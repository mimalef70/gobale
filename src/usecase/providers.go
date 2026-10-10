package usecase

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"time"
)

func (s *Service) Providers() []domains.ProviderDescriptor {
	return s.options.Providers.List()
}
func (s *Service) providerEnabled(p domains.Provider) bool {
	r, err := s.options.Providers.Get(p)
	return err == nil && r.Contract.Descriptor().Enabled
}
func (s *Service) provider(p domains.Provider) (domains.ProviderContract, error) {
	r, err := s.options.Providers.Get(p)
	if err != nil {
		return nil, err
	}
	if !r.Contract.Descriptor().Enabled {
		return nil, domains.Unsupported(string(p))
	}
	return r.Contract, nil
}
func (s *Service) Capabilities(p domains.Provider) ([]domains.OperationContract, error) {
	r, err := s.options.Providers.Get(p)
	if err != nil {
		return nil, err
	}
	return r.Contract.Operations(), nil
}
func (s *Service) DeviceCapabilities(ctx context.Context, id string) ([]domains.OperationContract, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.Capabilities(d.Provider)
}
func operationDefinition(c domains.ProviderContract, name string) (domains.OperationContract, bool) {
	for _, def := range c.Operations() {
		if def.Operation == name {
			return def, true
		}
	}
	return domains.OperationContract{}, false
}
func (s *Service) normalizeMutation(d domains.Device, operation string, payload json.RawMessage) (json.RawMessage, domains.Peer, error) {
	c, err := s.provider(d.Provider)
	if err != nil {
		return nil, domains.Peer{}, err
	}
	def, ok := operationDefinition(c, operation)
	if !ok || def.Mode != "mutation" {
		return nil, domains.Peer{}, domains.Unsupported(operation)
	}
	return c.NormalizeOperation(operation, payload)
}
func (s *Service) validateSend(d domains.Device, r domains.SendRequest) error {
	c, err := s.provider(d.Provider)
	if err != nil {
		return err
	}
	return c.ValidateSend(r)
}
func validateFilter(c domains.ProviderContract, f domains.WebhookFilter) error {
	if err := f.Validate(); err != nil {
		return err
	}
	for _, kind := range f.PeerTypes {
		if !domains.ValidProviderPeerType(c, kind) {
			return domains.E("INVALID_WEBHOOK_FILTER", "peer type is invalid for this provider", 400)
		}
	}
	for _, list := range [][]domains.Peer{f.Peers, f.ExcludePeers} {
		for _, p := range list {
			if c.ValidatePeer(p) != nil {
				return domains.E("INVALID_WEBHOOK_FILTER", "peer is invalid for this provider", 400)
			}
		}
	}
	for _, list := range [][]string{f.SenderIDs, f.ExcludeSenderIDs} {
		for _, id := range list {
			if !domains.ValidProviderSender(c, id) {
				return domains.E("INVALID_WEBHOOK_FILTER", "sender is invalid for this provider", 400)
			}
		}
	}
	return nil
}
func (s *Service) connect(ctx context.Context, d domains.Device, c domains.Client, session *domains.Session) error {
	contract, err := s.provider(d.Provider)
	if err != nil {
		return err
	}
	if err = contract.ValidateSession(session); err != nil {
		return err
	}
	if batch, ok := c.(domains.BatchClient); ok {
		return batch.ConnectBatch(ctx, session, s.batchSink(d.ConnectionID))
	}
	return c.Connect(ctx, session, s.sink(d.ConnectionID))
}

// The entry lock must be held. Retire rotation callbacks and join the old
// connection before reading its final durable session. A callback from that
// connection must never overwrite a token installed by the next generation.
func (s *Service) stopForReconnect(ctx context.Context, connection string, e *clientEntry) (*domains.Session, error) {
	e.setPersistenceActive(false)
	if err := e.client.Disconnect(ctx); err != nil {
		return nil, safeError(err)
	}
	session, err := s.store.LoadSession(ctx, connection)
	if err != nil {
		return nil, err
	}
	e.attachSessionPersistence(e.client)
	return session, nil
}

func (s *Service) batchSink(connection string) domains.BatchSink {
	return func(ctx context.Context, batch domains.EventBatch) error {
		if len(batch.Events) > 4097 || len(batch.Checkpoints) > 4097 {
			return domains.E("INVALID_EVENT_BATCH", "provider batch exceeds bounds", 502)
		}
		d, err := s.store.DeviceByConnection(ctx, connection)
		if err != nil {
			return err
		}
		for i := range batch.Events {
			event := &batch.Events[i]
			if event.ID == "" || event.Type == "" {
				return domains.E("INVALID_EVENT", "provider event requires stable identity and type", 502)
			}
			if event.Provider != d.Provider || (event.AccountID != "" && event.AccountID != d.AccountID) {
				return domains.E("EVENT_ACCOUNT_MISMATCH", "event does not match connection", 409)
			}
			if err := event.ValidateMessageProjection(); err != nil {
				return err
			}
			event.SessionID = d.ID
			event.AccountID = d.AccountID
			if len(event.Payload) == 0 {
				event.Payload = json.RawMessage(`{}`)
			}
			if !json.Valid(event.Payload) {
				return domains.E("INVALID_EVENT", "provider event payload is not valid JSON", 502)
			}
		}
		// Event filtering happens inside the storage acceptance transaction.
		targets := []storage.WebhookTarget{{Device: true, CurrentDevice: true, MergeGlobal: s.options.MergeGlobal}}
		for _, target := range s.options.GlobalWebhooks {
			target.Events = s.options.GlobalWebhookEvents
			targets = append(targets, target)
		}
		_, err = s.store.AppendBatch(ctx, connection, batch, targets)
		return err
	}
}

func (s *Service) ValidateSend(ctx context.Context, id string, r domains.SendRequest) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	return s.validateSend(d, r)
}

func (s *Service) validateQueryIdentity(d domains.Device, peer, sender string) error {
	registration, err := s.options.Providers.Get(d.Provider)
	c := registration.Contract
	if err != nil {
		return err
	}
	if peer != "" {
		p, err := domains.FilterPeer(peer)
		if err != nil {
			return err
		}
		if c.ValidatePeer(p) != nil || (d.Provider == domains.ProviderBale && !c.ValidateUserID(p.ID)) {
			return domains.E("INVALID_FILTER", "peer is invalid for this provider", 400)
		}
	}
	if sender != "" && !domains.ValidProviderSender(c, sender) {
		return domains.E("INVALID_FILTER", "sender is invalid for this provider", 400)
	}
	return nil
}

// Rotation callbacks belong to one client generation. They can update an
// existing durable session, but can never recreate one after local logout.
func (e *clientEntry) setPersistenceActive(active bool) {
	e.persistMu.Lock()
	e.persistActive = active
	e.persistMu.Unlock()
}
func (e *clientEntry) attachSessionPersistence(client domains.Client) {
	e.persistMu.Lock()
	e.persistGeneration++
	generation := e.persistGeneration
	e.persistActive = false
	e.persistMu.Unlock()
	if native, ok := client.(domains.SessionPersistenceClient); ok {
		native.SetSessionPersister(func(ctx context.Context, next *domains.Session) error {
			e.persistMu.Lock()
			defer e.persistMu.Unlock()
			if !e.persistActive || generation != e.persistGeneration || e.persistSession == nil {
				return domains.E("SESSION_RETIRED", "session generation is no longer active", 409)
			}
			return e.persistSession(ctx, next)
		})
	}
}

func (s *Service) ActiveProviders(ctx context.Context) ([]domains.Provider, error) {
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	present := map[domains.Provider]bool{}
	for _, d := range devices {
		present[d.Provider] = true
	}
	out := make([]domains.Provider, 0, len(present))
	for _, p := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
		if present[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

func (e *clientEntry) refreshChallenge(challenge domains.Challenge) {
	if challenge.ID == "" {
		return
	}
	now := time.Now().UTC()
	expires := challenge.ExpiresAt
	if expires.IsZero() || expires.After(now.Add(10*time.Minute)) {
		expires = now.Add(10 * time.Minute)
	}
	resend := now
	if challenge.ResendAfterSeconds != nil && *challenge.ResendAfterSeconds > 0 {
		seconds := *challenge.ResendAfterSeconds
		if seconds > 86400 {
			seconds = 86400
		}
		resend = now.Add(time.Duration(seconds) * time.Second)
	}
	e.authMetaMu.Lock()
	defer e.authMetaMu.Unlock()
	masked := ""
	if e.challenge != nil {
		masked = e.challenge.MaskedPhone
	}
	e.challenge = &domains.PublicChallenge{ID: challenge.ID, ExpiresAt: expires, ResendAvailableAt: resend, MaskedPhone: masked, Delivery: challenge.Delivery, NextDelivery: challenge.NextDelivery, AvailableDeliveries: append([]string{}, challenge.AvailableDeliveries...)}
	e.resendAvailableAt = resend
}
