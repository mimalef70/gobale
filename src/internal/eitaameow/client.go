package eitaameow

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

const sessionVersion = 1

// Config is host configuration, never consumer input. Endpoint overrides must
// not be accepted from an API caller. Session callbacks must commit before return.
type Config struct {
	Endpoint           string
	UploadEndpoint     string
	DownloadEndpoint   string
	HTTPClient         *http.Client
	APIID              int32
	APIHash            string
	ConnectionID       string
	PollInterval       time.Duration
	PersistSession     domains.SessionPersister
	LoadCheckpoint     func(context.Context, string) (string, error)
	MediaSource        domains.MediaSource
	MaxMediaBytes      int64
	PollPermit         func(context.Context) (func(), error)
	SaveMediaReference func(context.Context, domains.Peer, string, domains.ProviderMedia) (bool, error)
}
type privateSession struct {
	Token  string            `json:"token"`
	IMEI   string            `json:"imei"`
	UserID string            `json:"user_id"`
	Peers  map[string]object `json:"peers,omitempty"`
}
type authChallenge struct {
	ID, Phone, Hash string
	Code            string // private, short-lived binding for the observed password2 login
	Expires         time.Time
	Password        bool
	Public          domains.Challenge
}
type Client struct {
	cfg              Config
	codec            *codec
	http             *http.Client
	mu               sync.RWMutex
	authMu           sync.Mutex
	persistMu        sync.Mutex
	session          privateSession
	challenge        authChallenge
	status           domains.ConnectionStatus
	cancel           context.CancelFunc
	done             chan struct{}
	rpcSlots         chan struct{}
	channelCursor    string
	channelGapReason string
	channelMore      bool
}

func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://hasan.eitaa.ir/eitaa/"
	}
	if cfg.UploadEndpoint == "" {
		if cfg.HTTPClient != nil {
			cfg.UploadEndpoint = cfg.Endpoint
		} else {
			cfg.UploadEndpoint = "https://alzheimer.eitaa.com/eitaa/"
		}
	}
	if cfg.DownloadEndpoint == "" {
		if cfg.HTTPClient != nil {
			cfg.DownloadEndpoint = cfg.Endpoint
		} else {
			cfg.DownloadEndpoint = "https://mohsen.eitaa.com/eitaa/"
		}
	}
	for _, endpoint := range []string{cfg.UploadEndpoint, cfg.DownloadEndpoint} {
		u, e := url.Parse(endpoint)
		if e != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(cfg.HTTPClient != nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
			return nil, errors.New("invalid Eitaa media endpoint")
		}
	}
	if cfg.MaxMediaBytes == 0 {
		cfg.MaxMediaBytes = 64 << 20
	}
	if cfg.MaxMediaBytes < 1 || cfg.MaxMediaBytes > 1<<30 {
		return nil, errors.New("invalid Eitaa media limit")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return nil, errors.New("invalid Eitaa endpoint")
	}
	if u.Scheme != "https" && !(cfg.HTTPClient != nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return nil, errors.New("Eitaa endpoint requires HTTPS")
	}
	if cfg.APIID == 0 {
		cfg.APIID = 2496
	}
	if cfg.APIHash == "" {
		cfg.APIHash = "8da85b0d5bfe62527e5b244c209159c3"
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.PollInterval < time.Second {
		return nil, errors.New("Eitaa polling interval must be at least one second")
	}
	h := http.Client{Timeout: 45 * time.Second}
	if cfg.HTTPClient != nil {
		h = *cfg.HTTPClient
	}
	if h.Timeout <= 0 || h.Timeout > 45*time.Second {
		h.Timeout = 45 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c, e := bundledCodec()
	if e != nil {
		return nil, e
	}
	imei, e := randomHex(8)
	if e != nil {
		return nil, e
	}
	return &Client{cfg: cfg, codec: c, http: &h, session: privateSession{IMEI: imei + "__web", Peers: map[string]object{}}, status: domains.ConnectionStatus{Auth: "unauthenticated", Transport: "disconnected", Recovery: "not_started"}, rpcSlots: make(chan struct{}, 4)}, nil
}
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}

func (c *Client) SetSessionPersister(p domains.SessionPersister) {
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.cfg.PersistSession = p
}
func (o object) str(k string) string { s, _ := o[k].(string); return s }
func (o object) num(k string) int64  { n, _ := integer(o[k]); return n }
func asObject(v any) object          { o, _ := v.(object); return o }
func asObjects(v any) []object {
	var out []object
	if values, ok := v.([]any); ok {
		for _, x := range values {
			if o := asObject(x); o != nil {
				out = append(out, o)
			}
		}
	}
	return out
}
func (c *Client) Status() domains.ConnectionStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}
func (c *Client) setStatus(auth, transport, recovery, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if auth != "" {
		c.status.Auth = auth
	}
	if transport != "" {
		c.status.Transport = transport
	}
	if recovery != "" {
		c.status.Recovery = recovery
		if c.status.RecoveryIssue == "difference_too_long" || c.status.RecoveryIssue == "channel_difference_too_long" || c.status.RecoveryIssue == "unresolved_message_peer" {
			c.status.Recovery = "gap_detected"
		}
	}
	c.status.LastError = code
}
func protocolError() error {
	return domains.E("PROVIDER_PROTOCOL_ERROR", "Eitaa returned an unsupported response", 502)
}

// invoke deliberately performs one attempt. A transport failure after a write
// starts is ambiguous even if a different endpoint might accept another attempt.
func (c *Client) invokeOnce(ctx context.Context, method string, params object, mutating, anonymous bool) (object, error) {
	return c.invokeProfile(ctx, method, params, mutating, anonymous, false)
}

// An upload transaction stays on one pinned media profile, including its final write.
func (c *Client) invokeUpload(ctx context.Context, method string, params object) (object, error) {
	return c.invokeWithProfile(ctx, method, params, true, false, true)
}

func (c *Client) invokeProfile(ctx context.Context, method string, params object, mutating, anonymous, upload bool) (object, error) {
	select {
	case c.rpcSlots <- struct{}{}:
		defer func() { <-c.rpcSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	inner, err := c.codec.encodeMethod(method, params)
	if err != nil {
		return nil, domains.E("PROVIDER_REQUEST_INVALID", "invalid native Eitaa request", 400)
	}
	c.mu.RLock()
	token, imei := c.session.Token, c.session.IMEI
	c.mu.RUnlock()
	if anonymous {
		token = ""
	}
	if !anonymous && token == "" {
		return nil, domains.E("AUTH_REQUIRED", "account authentication is required", 401)
	}
	envelopeFlags := 32
	if upload {
		envelopeFlags = 128
	}
	body, err := c.codec.encodeMethod("eitaaObject", object{"token": token, "imei": imei, "packed_data": inner, "layer": 135, "flags": envelopeFlags})
	if err != nil {
		return nil, protocolError()
	}
	endpoint := c.cfg.Endpoint
	if upload {
		endpoint = c.cfg.UploadEndpoint
	}
	if method == "upload.getFile" {
		endpoint = c.cfg.DownloadEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, protocolError()
	}
	req.Header.Set("Origin", "https://web.eitaa.com")
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &domains.Error{Code: "PROVIDER_TRANSPORT_ERROR", Message: "Eitaa request did not complete", HTTP: 502, Ambiguous: mutating, Retryable: !mutating}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &domains.Error{Code: "PROVIDER_HTTP_ERROR", Message: "Eitaa returned an unsuccessful HTTP response", HTTP: 502, Ambiguous: mutating, Retryable: !mutating}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxWireBytes+1))
	if err != nil || len(raw) > maxWireBytes {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Eitaa response could not be decoded", HTTP: 502, Ambiguous: mutating}
	}
	value, err := c.codec.decodeResponse(method, raw)
	if err != nil {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Eitaa response could not be decoded", HTTP: 502, Ambiguous: mutating}
	}
	if ok, yes := value.(bool); yes {
		return object{"acknowledged": ok}, nil
	}
	if items, yes := value.([]any); yes {
		return object{"items": items}, nil
	}
	obj := asObject(value)
	if obj == nil {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Eitaa response could not be decoded", HTTP: 502, Ambiguous: mutating}
	}
	if obj.str("_") == "error" || obj.str("_") == "eitta_error" {
		return nil, rpcError(obj)
	}
	if obj.str("_") == "eitaa_updates_expire_token" || obj.str("_") == "eitaa_token_updating" {
		return nil, &domains.Error{Code: "SESSION_REFRESH_REQUIRED", Message: "Eitaa session requires renewal", HTTP: 503, Ambiguous: mutating}
	}
	return obj, nil
}
func rpcError(o object) error {
	text := o.str("text")
	code, status := "PROVIDER_REJECTED", 502
	switch text {
	case "SESSION_PASSWORD_NEEDED":
		code, status = "PASSWORD_REQUIRED", 401
	case "PHONE_CODE_INVALID":
		code, status = "INVALID_CODE", 400
	case "PHONE_CODE_EXPIRED":
		code, status = "CODE_EXPIRED", 400
	case "PASSWORD_HASH_INVALID":
		code, status = "INVALID_PASSWORD", 400
	case "NO_PROFILE_PHOTO":
		code, status = "AVATAR_NOT_FOUND", 404
	case "INVALID_CONSTRUCTOR":
		code, status = "FEATURE_NOT_SUPPORTED", 501
	case "INVALID_LOGIN":
		// The official web client renews this token; it does not discard the
		// authorized account. The renewal layer retries reads only.
		code, status = "SESSION_REFRESH_REQUIRED", 503
	case "AUTH_KEY_UNREGISTERED", "AUTH_KEY_INVALID", "SESSION_REVOKED", "SESSION_EXPIRED", "TOKEN_EXPIRED":
		code, status = "AUTH_REQUIRED", 401
	case "PHONE_NUMBER_INVALID":
		code, status = "INVALID_PHONE", 400
	case "PEER_ID_INVALID", "USER_ID_INVALID", "CHANNEL_INVALID":
		code, status = "PEER_NOT_FOUND", 404
	case "CHAT_WRITE_FORBIDDEN", "CHAT_ADMIN_REQUIRED", "USER_BANNED_IN_CHANNEL":
		code, status = "PROVIDER_FORBIDDEN", 403
	}
	e := &domains.Error{Code: code, Message: "Eitaa rejected the request", HTTP: status}
	if strings.HasPrefix(text, "FLOOD_WAIT_") {
		n, err := strconv.ParseInt(strings.TrimPrefix(text, "FLOOD_WAIT_"), 10, 32)
		if err == nil && n > 0 {
			e.Code = "RATE_LIMITED"
			e.HTTP = 429
			e.RetryAfterSeconds = n
		}
	}
	return e
}
func (c *Client) StartAuth(ctx context.Context, phone string) (domains.Challenge, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	phone = strings.TrimPrefix(phone, "+")
	if len(phone) < 7 || len(phone) > 16 {
		return domains.Challenge{}, domains.E("INVALID_PHONE", "international phone number is required", 400)
	}
	for _, r := range phone {
		if r < '0' || r > '9' {
			return domains.Challenge{}, domains.E("INVALID_PHONE", "international phone number is required", 400)
		}
	}
	response, err := c.invoke(ctx, "auth.sendCode", object{"phone_number": phone, "api_id": c.cfg.APIID, "api_hash": c.cfg.APIHash, "settings": object{"_": "codeSettings"}}, true, true)
	if err != nil {
		return domains.Challenge{}, err
	}
	if response.str("_") != "auth.sentCode" || response.str("phone_code_hash") == "" {
		return domains.Challenge{}, protocolError()
	}
	id, err := randomHex(16)
	if err != nil {
		return domains.Challenge{}, err
	}
	c.challenge = authChallenge{ID: id, Phone: phone, Hash: response.str("phone_code_hash"), Expires: time.Now().Add(10 * time.Minute)}
	c.setStatus("awaiting_code", "disconnected", "not_started", "")
	ch := domains.Challenge{ID: id, State: "awaiting_code", ExpiresAt: c.challenge.Expires}
	ch.Delivery = codeDelivery(asObject(response["type"]).str("_"))
	if next := asObject(response["next_type"]); next != nil {
		ch.NextDelivery = codeDelivery(next.str("_"))
	}
	for _, delivery := range []string{ch.Delivery, ch.NextDelivery} {
		if delivery != "" && delivery != "unknown" && (len(ch.AvailableDeliveries) == 0 || ch.AvailableDeliveries[0] != delivery) {
			ch.AvailableDeliveries = append(ch.AvailableDeliveries, delivery)
		}
	}
	if timeout, ok := response["timeout"]; ok {
		n, e := integer(timeout)
		if e != nil || n < 0 {
			return domains.Challenge{}, protocolError()
		}
		ch.ResendAfterSeconds = &n
	}
	c.challenge.Public = ch
	return c.publicChallenge(), nil
}
func (c *Client) validChallenge(id string) error {
	if c.challenge.ID == "" || id != c.challenge.ID || !time.Now().Before(c.challenge.Expires) {
		return domains.E("CHALLENGE_EXPIRED", "login challenge is missing or expired", 400)
	}
	return nil
}
func (c *Client) SubmitCode(ctx context.Context, id, code string) (*domains.Session, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if err := c.validChallenge(id); err != nil {
		return nil, err
	}
	if code == "" || len(code) > 32 {
		return nil, domains.E("INVALID_CODE", "code is required", 400)
	}
	o, err := c.invoke(ctx, "auth.signIn", object{"phone_number": c.challenge.Phone, "phone_code_hash": c.challenge.Hash, "phone_code": code}, true, true)
	if err != nil {
		var e *domains.Error
		if errors.As(err, &e) && e.Code == "PASSWORD_REQUIRED" {
			c.challenge.Password = true
			c.challenge.Code = code
			c.challenge.Public.State = "awaiting_password"
			c.setStatus("awaiting_password", "disconnected", "not_started", "")
		}
		return nil, err
	}
	if o.str("_") == "auth.authorizationSignUpRequired" {
		return nil, domains.E("SIGNUP_REQUIRED", "this phone number does not have an Eitaa account", 409)
	}
	return c.acceptAuthorization(ctx, o)
}
func (c *Client) SubmitPassword(ctx context.Context, id, password string) (*domains.Session, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if err := c.validChallenge(id); err != nil {
		return nil, err
	}
	if !c.challenge.Password {
		return nil, domains.E("INVALID_AUTH_STATE", "password is not requested", 409)
	}
	if password == "" || len(password) > 1024 {
		return nil, domains.E("INVALID_PASSWORD", "password is required", 400)
	}
	o, err := c.invoke(ctx, "account.getPassword", object{}, false, true)
	if err != nil {
		return nil, err
	}
	method, params, err := passwordCheck(password, o, c.challenge)
	if err != nil {
		return nil, protocolError()
	}
	result, err := c.invoke(ctx, method, params, true, true)
	if err != nil {
		return nil, err
	}
	return c.acceptAuthorization(ctx, result)
}
func (c *Client) acceptAuthorization(ctx context.Context, o object) (*domains.Session, error) {
	if o.str("_") != "auth.authorization" || o.str("token") == "" {
		return nil, protocolError()
	}
	user := asObject(o["user"])
	if user.num("id") <= 0 {
		return nil, protocolError()
	}
	c.mu.RLock()
	next := c.session
	c.mu.RUnlock()
	next.Token = o.str("token")
	next.UserID = strconv.FormatInt(user.num("id"), 10)
	next.Peers = map[string]object{}
	data, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	s := &domains.Session{Provider: domains.ProviderEitaa, Version: sessionVersion, UserID: next.UserID, Phone: c.challenge.Phone, Data: data}
	// Initial authorization is returned to the gateway, which commits it before
	// reporting success or connecting. The persistence callback is update-only
	// and reserved for changes to an already committed authenticated session.
	c.mu.Lock()
	c.session = next
	c.mu.Unlock()
	c.challenge = authChallenge{}
	c.setStatus("authenticated", "disconnected", "not_started", "")
	return s, nil
}
func (c *Client) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.setStatus("", "disconnected", "", "")
	return nil
}
func (c *Client) Logout(ctx context.Context) error {
	if err := c.Disconnect(ctx); err != nil {
		return err
	}
	_, err := c.invoke(ctx, "auth.logOut", object{}, true, false)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.session.Token = ""
	c.session.Peers = map[string]object{}
	c.mu.Unlock()
	c.setStatus("unauthenticated", "disconnected", "not_started", "")
	return nil
}
