// Package usecase owns provider-independent connection and delivery orchestration.
package usecase

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
)

// Options controls bounded background work. The gateway must remain behind an
// authenticated service boundary; organization/operator authorization belongs to the consuming application.
type Options struct {
	GlobalWebhooks      []storage.WebhookTarget
	GlobalWebhookEvents []string
	MergeGlobal         bool
	QueueLimit          int
	PollInterval        time.Duration
	WebhookClient       *http.Client
	WebhookWorkers      int
	SendWorkers         int
	SendTimeout         time.Duration
	ReconnectWorkers    int
}

type clientEntry struct {
	mu                sync.Mutex   // serializes sends and lifecycle transitions for this account
	clientMu          sync.RWMutex // short lock for status snapshots when lifecycle work is in progress
	client            domains.Client
	authMetaMu        sync.Mutex // public challenge snapshots must not block on provider calls
	challenge         *domains.PublicChallenge
	resendAvailableAt time.Time // provider cooldown survives local challenge expiry
	desired           bool
	closed            bool
	failures          int
	nextConnect       time.Time
}

type Service struct {
	store          *storage.Store
	options        Options
	factory        domains.ClientFactory
	lifecycleMu    sync.Mutex
	mu             sync.RWMutex
	clients        map[string]*clientEntry
	ctx            context.Context
	cancel         context.CancelFunc
	started        bool
	closed         bool
	wg             sync.WaitGroup
	done           chan struct{}
	reconnectSlots chan struct{}
	metrics        workerMetrics
}

func New(store *storage.Store, options Options, factory domains.ClientFactory) *Service {
	if options.QueueLimit <= 0 {
		options.QueueLimit = 1000
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 500 * time.Millisecond
	}
	if options.WebhookWorkers <= 0 {
		options.WebhookWorkers = 8
	}
	if options.SendWorkers <= 0 {
		options.SendWorkers = 4
	}
	if options.ReconnectWorkers <= 0 {
		options.ReconnectWorkers = 4
	}
	if options.ReconnectWorkers > 4 {
		options.ReconnectWorkers = 4
	}
	if options.SendTimeout <= 0 {
		options.SendTimeout = 40 * time.Second
	}
	if options.WebhookClient == nil {
		options.WebhookClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		copyClient := *options.WebhookClient
		if copyClient.Timeout <= 0 || copyClient.Timeout > 10*time.Second {
			copyClient.Timeout = 10 * time.Second
		}
		copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		options.WebhookClient = &copyClient
	}
	return &Service{store: store, options: options, factory: factory, clients: make(map[string]*clientEntry), done: make(chan struct{}), reconnectSlots: make(chan struct{}, options.ReconnectWorkers)}
}

func (s *Service) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return domains.E("SERVICE_CLOSED", "service is closed", 503)
	}
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.store == nil || s.factory == nil {
		s.mu.Unlock()
		return domains.E("SERVICE_CONFIGURATION", "store and provider factory are required", 500)
	}
	s.mu.Unlock()
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		entry, err := s.entry(device)
		if err != nil {
			return err
		}
		entry.mu.Lock()
		session, loadErr := s.store.LoadSession(ctx, device.ConnectionID)
		if loadErr == nil {
			entry.desired = session != nil
		}
		entry.mu.Unlock()
		if loadErr != nil {
			return loadErr
		}
	}
	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.mu.Unlock()
	for n := 0; n < s.options.SendWorkers; n++ {
		s.wg.Add(1)
		go s.sendLoop()
	}
	for n := 0; n < s.options.WebhookWorkers; n++ {
		s.wg.Add(1)
		go s.webhookLoop()
	}
	s.wg.Add(2)
	go s.scheduleLoop()
	go s.reconnectLoop()
	return nil
}

func (s *Service) Close(ctx context.Context) error {
	s.lifecycleMu.Lock()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		entries := make([]*clientEntry, 0, len(s.clients))
		for _, entry := range s.clients {
			entries = append(entries, entry)
		}
		go func() {
			s.wg.Wait()
			var disconnect sync.WaitGroup
			for _, entry := range entries {
				disconnect.Add(1)
				go func(e *clientEntry) {
					defer disconnect.Done()
					e.mu.Lock()
					defer e.mu.Unlock()
					e.closed = true
					e.clearAuthMetadata()
					e.desired = false
					callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = e.client.Disconnect(callCtx)
				}(entry)
			}
			disconnect.Wait()
			close(s.done)
		}()
	}
	s.mu.Unlock()
	s.lifecycleMu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) entry(device domains.Device) (*clientEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, domains.E("SERVICE_CLOSED", "service is closed", 503)
	}
	if s.factory == nil {
		return nil, domains.E("SERVICE_CONFIGURATION", "provider factory is required", 500)
	}
	entry := s.clients[device.ConnectionID]
	if entry == nil {
		entry = &clientEntry{client: s.factory(device)}
		s.clients[device.ConnectionID] = entry
	}
	return entry, nil
}
func (s *Service) ResolveDevice(ctx context.Context, id string) (domains.Device, error) {
	if selected, ok := ctx.Value(deviceScopeKey{}).(deviceScope); ok {
		if id != "" && id != selected.alias {
			return domains.Device{}, domains.E("DEVICE_SCOPE_MISMATCH", "request is bound to another device", 409)
		}
		return s.store.DeviceByConnection(ctx, selected.connection)
	}
	if id != "" {
		return s.store.GetDevice(ctx, id)
	}
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return domains.Device{}, err
	}
	if len(devices) != 1 {
		return domains.Device{}, domains.E("DEVICE_ID_REQUIRED", "select a device explicitly", 400)
	}
	return devices[0], nil
}
func (s *Service) CreateDevice(ctx context.Context, id string) (domains.Device, error) {
	if strings.TrimSpace(id) == "" {
		return domains.Device{}, domains.E("INVALID_DEVICE_ID", "device id is required", 400)
	}
	if len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "/\\\x00\r\n") {
		return domains.Device{}, domains.E("INVALID_DEVICE_ID", "device id must be 1–128 characters without path separators or surrounding whitespace", 400)
	}
	return s.store.CreateDevice(ctx, id)
}
func (s *Service) ListDevices(ctx context.Context) ([]domains.Device, error) {
	return s.store.ListDevices(ctx)
}
func (s *Service) GetDevice(ctx context.Context, id string) (domains.Device, error) {
	return s.ResolveDevice(ctx, id)
}
func (s *Service) DeleteDevice(ctx context.Context, id string) error {
	device, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	e, err := s.entry(device)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.desired = false
	e.closed = true
	e.clearAuthMetadata()
	if err := s.store.DeleteDevice(ctx, device.ConnectionID); err != nil {
		e.closed = false
		return err
	}
	// Deleting a local connection does not claim that the remote session was revoked.
	_ = e.client.Disconnect(ctx)
	s.mu.Lock()
	delete(s.clients, device.ConnectionID)
	s.mu.Unlock()
	return nil
}
func (s *Service) StartAuth(ctx context.Context, id, phone string) (domains.Challenge, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Challenge{}, err
	}
	if strings.TrimSpace(phone) == "" {
		return domains.Challenge{}, domains.E("INVALID_PHONE", "phone is required", 400)
	}
	e, err := s.entry(d)
	if err != nil {
		return domains.Challenge{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return domains.Challenge{}, domains.E("DEVICE_NOT_FOUND", "device no longer exists", 404)
	}
	session, err := s.store.LoadSession(ctx, d.ConnectionID)
	if err != nil {
		return domains.Challenge{}, err
	}
	if session != nil {
		return domains.Challenge{}, domains.E("ALREADY_AUTHENTICATED", "log out before starting a new authentication challenge", 409)
	}
	e.authMetaMu.Lock()
	now := time.Now().UTC()
	if now.Before(e.resendAvailableAt) {
		wait := int64((time.Until(e.resendAvailableAt) + time.Second - 1) / time.Second)
		e.authMetaMu.Unlock()
		return domains.Challenge{}, &domains.Error{Code: "AUTH_RESEND_TOO_SOON", Message: "wait before requesting another authentication code", HTTP: 429, RetryAfterSeconds: wait}
	}
	// Clear the previous public challenge before the replacement RPC. A failed
	// replacement cannot resurrect a challenge whose provider cookies were reset.
	e.challenge = nil
	e.authMetaMu.Unlock()
	e.desired = false
	challenge, err := e.client.StartAuth(ctx, phone)
	if err != nil {
		return domains.Challenge{}, safeError(err)
	}
	now = time.Now().UTC()
	if challenge.ID == "" {
		return domains.Challenge{}, domains.E("INVALID_PROVIDER_CHALLENGE", "provider did not return an authentication challenge", 502)
	}
	if challenge.ExpiresAt.IsZero() || challenge.ExpiresAt.After(now.Add(10*time.Minute)) {
		challenge.ExpiresAt = now.Add(10 * time.Minute)
	}
	resendAt := now
	if challenge.ResendAfterSeconds != nil {
		seconds := *challenge.ResendAfterSeconds
		if seconds < 0 || seconds > 86400 {
			return domains.Challenge{}, domains.E("INVALID_PROVIDER_CHALLENGE", "provider returned an invalid resend cooldown", 502)
		}
		resendAt = now.Add(time.Duration(seconds) * time.Second)
	}
	e.authMetaMu.Lock()
	e.resendAvailableAt = resendAt
	e.challenge = &domains.PublicChallenge{ID: challenge.ID, ExpiresAt: challenge.ExpiresAt, ResendAvailableAt: resendAt, SentCodeType: challenge.SentCodeType, NextSendCodeType: challenge.NextSendCodeType, AvailableSendCodeTypes: append([]int32{}, challenge.AvailableSendCodeTypes...), MaskedPhone: maskedPhone(phone)}
	e.authMetaMu.Unlock()
	return challenge, nil
}
func (s *Service) SubmitCode(ctx context.Context, id, challenge, code string) (domains.ConnectionStatus, error) {
	return s.submitAuth(ctx, id, challenge, code, false)
}
func (s *Service) SubmitPassword(ctx context.Context, id, challenge, password string) (domains.ConnectionStatus, error) {
	return s.submitAuth(ctx, id, challenge, password, true)
}
func (s *Service) submitAuth(ctx context.Context, id, challenge, value string, password bool) (domains.ConnectionStatus, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.ConnectionStatus{}, err
	}
	if challenge == "" || value == "" {
		return domains.ConnectionStatus{}, domains.E("INVALID_AUTH", "challenge and authentication value are required", 400)
	}
	e, err := s.entry(d)
	if err != nil {
		return domains.ConnectionStatus{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return domains.ConnectionStatus{}, domains.E("DEVICE_NOT_FOUND", "device no longer exists", 404)
	}
	public := e.publicChallenge(time.Now())
	if public == nil || public.ID != challenge {
		return e.adminStatus(time.Now()), domains.E("CHALLENGE_EXPIRED", "authentication challenge is absent or expired", 400)
	}
	var session *domains.Session
	if password {
		session, err = e.client.SubmitPassword(ctx, challenge, value)
	} else {
		session, err = e.client.SubmitCode(ctx, challenge, value)
	}
	if err != nil {
		var authErr *domains.Error
		if errors.As(err, &authErr) && authErr.Code == "CHALLENGE_EXPIRED" {
			e.authMetaMu.Lock()
			e.challenge = nil
			e.authMetaMu.Unlock()
		}
		return e.adminStatus(time.Now()), safeError(err)
	}
	if session == nil {
		return cleanStatus(e.client.Status()), nil
	}
	e.clearAuthMetadata()
	if session.UserID == "" || session.Token == "" {
		return cleanStatus(e.client.Status()), domains.E("INVALID_PROVIDER_SESSION", "provider did not return a complete session", 502)
	}
	if err = s.store.SaveSession(ctx, d.ConnectionID, session); err != nil {
		_ = e.client.Disconnect(ctx)
		e.desired = false
		e.setClient(s.factory(d))
		return cleanStatus(e.client.Status()), err
	}
	e.desired = true
	e.failures = 0
	e.nextConnect = time.Time{}
	err = e.client.Connect(ctx, session, s.sink(d.ConnectionID))
	if err != nil {
		e.failures++
		e.nextConnect = time.Now().Add(reconnectDelay(e.failures))
	}
	return cleanStatus(e.client.Status()), safeError(err)
}
func (s *Service) Status(ctx context.Context, id string) (domains.ConnectionStatus, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.ConnectionStatus{}, err
	}
	e, err := s.entry(d)
	if err != nil {
		return domains.ConnectionStatus{}, err
	}
	return e.adminStatus(time.Now()), nil
}
func (s *Service) Reconnect(ctx context.Context, id string) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	e, err := s.entry(d)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	session, err := s.store.LoadSession(ctx, d.ConnectionID)
	if err != nil {
		return err
	}
	if session == nil {
		return domains.E("AUTH_REQUIRED", "connect the account first", 409)
	}
	if e.closed {
		return domains.E("DEVICE_NOT_FOUND", "device no longer exists", 404)
	}
	e.desired = true
	if err = e.client.Disconnect(ctx); err != nil {
		return safeError(err)
	}
	err = e.client.Connect(ctx, session, s.sink(d.ConnectionID))
	if err != nil {
		e.failures++
		e.nextConnect = time.Now().Add(reconnectDelay(e.failures))
	} else {
		e.failures = 0
		e.nextConnect = time.Time{}
	}
	return safeError(err)
}
func (s *Service) Logout(ctx context.Context, id string) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	e, err := s.entry(d)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.desired = false
	e.clearAuthMetadata()
	remoteErr := e.client.Logout(ctx)
	_ = e.client.Disconnect(ctx)
	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = s.store.LogoutConnection(clearCtx, d.ConnectionID); err != nil {
		return err
	}
	e.setClient(s.factory(d)) // discard tokens and pending challenges retained after a failed remote revoke
	if remoteErr != nil {
		return domains.E("REMOTE_LOGOUT_UNCONFIRMED", "local session cleared; remote logout could not be confirmed", 502)
	}
	return nil
}

func (s *Service) Call(ctx context.Context, id, operation string, payload json.RawMessage) (json.RawMessage, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	e, err := s.entry(d)
	if err != nil {
		return nil, err
	}
	if isDurableMutation(operation) {
		return nil, domains.E("DURABLE_OPERATION_REQUIRED", "use the durable mutation endpoint with an Idempotency-Key", 409)
	}
	contract, extension := domains.OperationDefinition(operation)
	if !allowedCalls[operation] && !extension {
		return nil, domains.Unsupported(operation)
	}
	if len(payload) > 1<<20 || (len(payload) > 0 && !json.Valid(payload)) {
		return nil, domains.E("INVALID_REQUEST", "payload must be valid JSON up to 1 MiB", 400)
	}
	if extension {
		if contract.Mode != "read" && contract.Mode != "ephemeral" {
			return nil, domains.Unsupported(operation)
		}
		payload, _, err = domains.NormalizeOperation(operation, payload)
		if err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, domains.E("DEVICE_NOT_FOUND", "device no longer exists", 404)
	}
	result, err := e.client.Call(ctx, operation, payload)
	return result, safeError(err)
}

var allowedCalls = map[string]bool{
	"chat.history": true, "chat.messages": true, "chat.list": true, "chats": true,
	"group.list": true, "group.members": true, "contacts.list": true, "contacts.search": true,
	"group.info": true, "group.link": true, "account.info": true,
}

func (s *Service) GetWebhook(ctx context.Context, id string) (domains.WebhookConfig, error) {
	d, err := s.ResolveDevice(ctx, id)
	return d.Webhook, err
}
func (s *Service) PatchWebhook(ctx context.Context, id string, patch domains.WebhookPatch) (domains.WebhookConfig, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.WebhookConfig{}, err
	}
	if patch.URL != nil && *patch.URL != "" {
		if err = validateWebhookURL(*patch.URL); err != nil {
			return domains.WebhookConfig{}, err
		}
	}
	effectiveURL, effectiveSecret := d.Webhook.URL, d.Webhook.Secret
	if patch.URL != nil {
		effectiveURL = *patch.URL
	}
	if patch.Secret != nil {
		effectiveSecret = *patch.Secret
	}
	if effectiveURL != "" && strings.TrimSpace(effectiveSecret) == "" {
		return domains.WebhookConfig{}, domains.E("WEBHOOK_SECRET_REQUIRED", "configure a non-empty secret before enabling a device webhook", 400)
	}
	if patch.Secret != nil && len(*patch.Secret) > 4096 {
		return domains.WebhookConfig{}, domains.E("INVALID_WEBHOOK", "webhook secret exceeds 4096 bytes", 400)
	}
	if patch.Events != nil {
		if len(*patch.Events) > 100 {
			return domains.WebhookConfig{}, domains.E("INVALID_WEBHOOK", "too many event filters", 400)
		}
		for _, name := range *patch.Events {
			if strings.TrimSpace(name) == "" || len(name) > 128 {
				return domains.WebhookConfig{}, domains.E("INVALID_WEBHOOK", "invalid event filter", 400)
			}
		}
	}
	return s.store.PatchWebhook(ctx, d.ConnectionID, patch)
}
func validateWebhookURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return domains.E("INVALID_WEBHOOK", "webhook_url must be an HTTP(S) URL without credentials or fragment", 400)
	}
	return nil
}
func eventAllowed(event string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, filter := range filters {
		if filter == "*" || filter == event {
			return true
		}
	}
	return false
}
func (s *Service) targets(d domains.Device, event string) []storage.WebhookTarget {
	result := []storage.WebhookTarget{}
	if d.Webhook.URL != "" && eventAllowed(event, d.Webhook.Events) {
		result = append(result, storage.WebhookTarget{URL: d.Webhook.URL, Secret: d.Webhook.Secret, Revision: d.Webhook.Revision, Device: true})
	}
	if (d.Webhook.URL == "" || s.options.MergeGlobal) && eventAllowed(event, s.options.GlobalWebhookEvents) {
		for _, target := range s.options.GlobalWebhooks {
			duplicate := false
			for _, old := range result {
				if old.URL == target.URL {
					duplicate = true
					break
				}
			}
			if !duplicate {
				result = append(result, target)
			}
		}
	}
	return result
}
func (s *Service) sink(connection string) domains.Sink {
	return func(ctx context.Context, event domains.Event) error {
		d, err := s.store.DeviceByConnection(ctx, connection)
		if err != nil {
			return err
		}
		if event.ID == "" || event.Type == "" {
			return domains.E("INVALID_EVENT", "provider event must have a stable id and type", 502)
		}
		if event.AccountID != "" && d.AccountID != "" && event.AccountID != d.AccountID {
			return domains.E("EVENT_ACCOUNT_MISMATCH", "event account does not match connection", 409)
		}
		event.SessionID = d.ID
		event.AccountID = d.AccountID
		if event.Time.IsZero() {
			event.Time = time.Now().UTC()
		}
		if len(event.Payload) == 0 {
			event.Payload = json.RawMessage(`{}`)
		}
		if !json.Valid(event.Payload) {
			return domains.E("INVALID_EVENT", "provider event payload is not valid JSON", 502)
		}
		_, err = s.store.AppendEvent(ctx, connection, event, s.targets(d, event.Type))
		return err
	}
}
func (s *Service) Chats(ctx context.Context, id string, limit, offset int) ([]storage.Chat, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.store.ListChats(ctx, d.ConnectionID, limit, offset)
}
func (s *Service) Messages(ctx context.Context, id, peer string, limit, offset int) ([]domains.Event, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.store.ListEvents(ctx, d.ConnectionID, peer, limit, offset)
}
func page(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// Provider error text is not a safe public or persisted diagnostic: upstream
// responses can contain URLs, tokens or echoed credentials. Codes are retained.
func safeError(err error) error {
	if err == nil {
		return nil
	}
	var de *domains.Error
	if errors.As(err, &de) {
		copyErr := *de
		copyErr.Code = safeCode(copyErr.Code)
		copyErr.Message = publicErrorMessage(copyErr.Code)
		return &copyErr
	}
	if errors.Is(err, context.Canceled) {
		return domains.E("REQUEST_CANCELLED", "request was cancelled", 499)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &domains.Error{Code: "PROVIDER_TIMEOUT", Message: "provider request timed out", HTTP: 504}
	}
	return domains.E("PROVIDER_ERROR", "provider request failed", 502)
}
func publicErrorMessage(code string) string {
	switch code {
	case "FEATURE_NOT_SUPPORTED":
		return "feature is not verified for this provider"
	case "SEND_UNKNOWN":
		return "send outcome is unknown; do not resend without reconciliation"
	case "AUTH_REQUIRED":
		return "account authentication is required"
	case "INVALID_CODE", "AUTH_CODE_INVALID":
		return "authentication code is invalid"
	case "INVALID_PASSWORD", "AUTH_PASSWORD_INVALID":
		return "authentication password is invalid"
	case "CHALLENGE_EXPIRED":
		return "authentication challenge expired"
	case "AUTH_RESEND_TOO_SOON":
		return "wait before requesting another authentication code"
	case "CONNECTION_UNAVAILABLE":
		return "provider connection is unavailable"
	default:
		return "provider request failed (" + safeCode(code) + ")"
	}
}
func safeCode(code string) string {
	if code == "" || len(code) > 80 {
		return "PROVIDER_ERROR"
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return "PROVIDER_ERROR"
		}
	}
	return code
}
func cleanStatus(status domains.ConnectionStatus) domains.ConnectionStatus {
	switch status.LastError {
	case "", "CONNECT_FAILED", "CONNECTION_LOST", "SESSION_REVOKED", "HANDSHAKE_FAILED", "WRITE_FAILED", "PROTOCOL_ERROR", "UPDATE_PROTOCOL_ERROR", "HEARTBEAT_TIMEOUT", "HEARTBEAT_FAILED", "EVENT_CONTEXT_MISSING", "EVENT_PERSIST_FAILED", "EVENT_BACKPRESSURE", "EVENT_DRAIN_INCOMPLETE", "RECOVERY_FAILED", "RECOVERY_GAP":
		return status
	}
	for _, prefix := range []string{"WS_CLOSE_", "CONNECTION_CLOSED_"} {
		if strings.HasPrefix(status.LastError, prefix) && len(status.LastError) == len(prefix)+4 && strings.Trim(strings.TrimPrefix(status.LastError, prefix), "0123456789") == "" {
			return status
		}
	}
	status.LastError = "provider reported a connection error"
	return status
}
func reconnectDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 6 {
		failures = 6
	}
	base := time.Duration(1<<uint(failures-1)) * time.Second
	var b [8]byte
	_, _ = rand.Read(b[:])
	return base + time.Duration(binary.LittleEndian.Uint64(b[:])%uint64(base/4+1))
}
func (s *Service) wait() bool {
	timer := time.NewTimer(s.options.PollInterval)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func diagnostic(err error) string {
	if err == nil {
		return ""
	}
	var de *domains.Error
	if errors.As(err, &de) {
		return fmt.Sprintf("%s: %s", safeCode(de.Code), publicErrorMessage(de.Code))
	}
	return "provider operation failed"
}

func (e *clientEntry) setClient(client domains.Client) {
	e.clearAuthMetadata()
	e.clientMu.Lock()
	e.client = client
	e.clientMu.Unlock()
}
