package usecase

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	domainSend "github.com/mimalef70/goomni/src/domains/send"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/mimalef70/goomni/src/internal/eitaameow"
	"github.com/mimalef70/goomni/src/internal/rubikameow"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperationFiltersUseSelectedProviderCatalogEvenWhenDisabled(t *testing.T) {
	registry, err := domains.NewProviderRegistry(
		domains.ProviderRegistration{Contract: bale.Contract{}},
		domains.ProviderRegistration{Contract: eitaameow.Contract{}},
		domains.ProviderRegistration{Contract: rubikameow.Contract{}},
	)
	require.NoError(t, err)
	s, st := testService(t, Options{Providers: registry}, nil)
	ctx := context.Background()
	for _, provider := range []domains.Provider{domains.ProviderEitaa, domains.ProviderRubika} {
		d, err := st.CreateDevice(ctx, string(provider), provider)
		require.NoError(t, err)
		_, err = s.ListOperationsFiltered(ctx, d.ID, domains.OperationFilter{Operation: "message.pin"})
		require.NoError(t, err)
		_, err = s.ListSchedulesFiltered(ctx, d.ID, domains.ScheduleFilter{Operation: "message.pin"})
		require.NoError(t, err)
		_, err = s.ListOperationsFiltered(ctx, d.ID, domains.OperationFilter{Operation: "arbitrary.rpc"})
		codeIs(t, err, "INVALID_FILTER")
	}
}

type syntheticContract struct {
	bale.Contract
	p       domains.Provider
	enabled bool
}

func (c syntheticContract) Descriptor() domains.ProviderDescriptor {
	return domains.ProviderDescriptor{ID: c.p, Name: string(c.p), Enabled: c.enabled, Verification: "synthetic"}
}
func (c syntheticContract) ValidatePeer(p domains.Peer) error        { return p.Validate() }
func (c syntheticContract) ValidateUserID(id string) bool            { return domains.ValidOpaqueID(id) }
func (c syntheticContract) ValidateSend(r domains.SendRequest) error { return r.Validate() }
func (c syntheticContract) ValidateSession(s *domains.Session) error {
	if s == nil || s.Provider != c.p || s.Version != 1 || !domains.ValidOpaqueID(s.UserID) {
		return domains.E("INVALID_SESSION", "synthetic invalid session", 400)
	}
	return nil
}
func (c syntheticContract) Operations() []domains.OperationContract {
	return []domains.OperationContract{{Operation: "message.forward", Schedulable: true, Mode: "mutation", Request: domains.FieldSchema{Type: "object", Properties: map[string]domains.FieldSchema{"peer": {Type: "object", Properties: map[string]domains.FieldSchema{"type": {Type: "string"}, "id": {Type: "string"}}, Required: []string{"type", "id"}}, "message_id": {Type: "string"}}, Required: []string{"peer", "message_id"}}}}
}
func (c syntheticContract) NormalizeOperation(op string, raw json.RawMessage) (json.RawMessage, domains.Peer, error) {
	if op != "message.forward" {
		return nil, domains.Peer{}, domains.Unsupported(op)
	}
	return domains.NormalizeOperationContract(c.Operations()[0], raw)
}

func TestProviderSelectionAdmissionAndIdenticalIDsAreIsolated(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	registrations := []domains.ProviderRegistration{}
	for _, p := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
		registrations = append(registrations, domains.ProviderRegistration{Contract: syntheticContract{p: p, enabled: true}, Factory: func(d domains.Device) domains.Client { calls.Add(1); return &fakeClient{status: readyStatus()} }})
	}
	registry, err := domains.NewProviderRegistry(registrations...)
	require.NoError(t, err)
	s, store := testService(t, Options{Providers: registry}, nil)
	ids := map[string]bool{}
	for _, p := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
		d, _, err := s.ProvisionDevice(ctx, domains.ProvisionDeviceRequest{Provider: p, DeviceID: string(p)}, "provision-"+string(p))
		require.NoError(t, err)
		request := domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "same-guid"}, Text: "fixture", Mentions: []string{"same-guid"}}
		op, err := s.Send(ctx, d.ID, request, "same-key")
		require.NoError(t, err)
		require.Equal(t, p, op.Provider)
		require.False(t, ids[op.ID])
		ids[op.ID] = true
		other := request
		other.Text = "different"
		_, err = s.Send(ctx, d.ID, other, "same-key")
		codeIs(t, err, "IDEMPOTENCY_CONFLICT")
		_, err = s.Mutate(ctx, d.ID, "account.name", json.RawMessage(`{"name":"fixture"}`), "unsupported")
		codeIs(t, err, "FEATURE_NOT_SUPPORTED")
		scheduled := domains.SendRequest{Kind: "operation", Operation: "send.poll", Payload: json.RawMessage(`{}`), ScheduleOptions: domainSend.ScheduleOptions{ScheduledAt: time.Now().Add(time.Hour).Format(time.RFC3339), Timezone: "UTC"}}
		_, err = s.CreateScheduleIdempotent(ctx, d.ID, scheduled, "unsupported-schedule")
		codeIs(t, err, "FEATURE_NOT_SUPPORTED")
		event := domains.Event{Provider: p, ID: "same-event", Type: "message", Peer: request.Peer, SenderID: "same-guid", Direction: "incoming", Time: time.Now().UTC(), Payload: json.RawMessage(`{"message":"fixture"}`)}
		require.NoError(t, s.batchSink(d.ConnectionID)(ctx, domains.EventBatch{Events: []domains.Event{event}, Checkpoints: []domains.CheckpointTransition{{Scope: "messages", Next: "cursor-1"}}}))
		events, err := s.EventsFiltered(ctx, d.ID, domains.EventFilter{Peer: "user:same-guid", SenderID: "same-guid"})
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.Equal(t, p, events[0].Provider)
		event.Provider = domains.Provider("wrong")
		require.Error(t, s.sink(d.ConnectionID)(ctx, event))
		rows, err := store.ListOperations(ctx, d.ConnectionID, 100, 0)
		require.NoError(t, err)
		require.Len(t, rows, 1)
	}
	require.Zero(t, calls.Load(), "admission and local search must not contact providers")
	_, _, err = s.ProvisionDevice(ctx, domains.ProvisionDeviceRequest{DeviceID: "missing"}, "missing")
	codeIs(t, err, "INVALID_PROVIDER")
	_, _, err = s.ProvisionDevice(ctx, domains.ProvisionDeviceRequest{Provider: domains.ProviderRubika, DeviceID: "bale"}, "provision-bale")
	codeIs(t, err, "IDEMPOTENCY_CONFLICT")
}

type rotatingClient struct {
	fakeClient
	persist   domains.SessionPersister
	challenge domains.Challenge
}

func (c *rotatingClient) SetSessionPersister(p domains.SessionPersister) { c.persist = p }
func (c *rotatingClient) CurrentChallenge() domains.Challenge            { return c.challenge }
func TestSessionRotationCannotRestoreLoggedOutOrReplacedClient(t *testing.T) {
	ctx := context.Background()
	clients := []*rotatingClient{}
	s, store := testService(t, Options{}, func(domains.Device) domains.Client {
		c := &rotatingClient{}
		c.authSession = &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "42", Token: "initial"}
		clients = append(clients, c)
		return c
	})
	d := mustDevice(t, s, "rotate")
	_, err := s.StartAuth(ctx, d.ID, "+10000000000")
	require.NoError(t, err)
	_, err = s.SubmitCode(ctx, d.ID, "challenge", "code")
	require.NoError(t, err)
	old := clients[0].persist
	next := &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "42", Token: "rotated"}
	require.NoError(t, old(ctx, next))
	saved, err := store.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, "rotated", saved.Token)
	require.NoError(t, s.Logout(ctx, d.ID))
	require.Error(t, old(ctx, next))
	saved, err = store.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Nil(t, saved)
	_, err = s.StartAuth(ctx, d.ID, "+10000000000")
	require.NoError(t, err)
	_, err = s.SubmitCode(ctx, d.ID, "challenge", "code")
	require.NoError(t, err)
	require.Error(t, old(ctx, next))
	saved, err = store.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, "initial", saved.Token)
}
func TestPasswordThenOTPContinuationRefreshesChallenge(t *testing.T) {
	ctx := context.Background()
	client := &rotatingClient{challenge: domains.Challenge{ID: "new-local-id", State: "awaiting_code", Delivery: "sms", ExpiresAt: time.Now().Add(time.Minute), AvailableDeliveries: []string{"sms"}}}
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return client })
	d := mustDevice(t, s, "continuation")
	client.status = domains.ConnectionStatus{Auth: "awaiting_code", Transport: "disconnected", Recovery: "degraded"}
	_, err := s.StartAuth(ctx, d.ID, "+10000000000")
	require.NoError(t, err)
	status, err := s.SubmitPassword(ctx, d.ID, "challenge", "synthetic")
	require.NoError(t, err)
	require.Equal(t, "awaiting_code", status.Auth)
	snapshot, err := s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Challenge)
	require.Equal(t, "new-local-id", snapshot.Challenge.ID)
	require.Equal(t, "sms", snapshot.Challenge.Delivery)
}

func TestDisabledProviderKeepsLocalHistoryAndRetirementAvailable(t *testing.T) {
	ctx := context.Background()
	registry, err := domains.NewProviderRegistry(domains.ProviderRegistration{Contract: syntheticContract{p: domains.ProviderEitaa, enabled: false}})
	require.NoError(t, err)
	s, store := testService(t, Options{Providers: registry}, nil)
	d, err := store.CreateDevice(ctx, "disabled", domains.ProviderEitaa)
	require.NoError(t, err)
	require.NoError(t, store.SaveSession(ctx, d.ConnectionID, &domains.Session{Provider: domains.ProviderEitaa, Version: 1, UserID: "opaque-user", Data: json.RawMessage(`{"token":"synthetic"}`)}))
	event := domains.Event{Provider: domains.ProviderEitaa, ID: "retained", Type: "message", Peer: domains.Peer{Type: "user", ID: "opaque-peer"}, Time: time.Now(), Payload: json.RawMessage(`{"message":"retained"}`)}
	require.NoError(t, s.batchSink(d.ConnectionID)(ctx, domains.EventBatch{Events: []domains.Event{event}}))
	status, err := s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, "PROVIDER_DISABLED", status.LastError)
	rows, err := s.EventsFiltered(ctx, d.ID, domains.EventFilter{Peer: "user:opaque-peer"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	catalog, err := s.Capabilities(domains.ProviderEitaa)
	require.NoError(t, err)
	require.NotEmpty(t, catalog)
	codeIs(t, s.Logout(ctx, d.ID), "REMOTE_LOGOUT_UNCONFIRMED")
	session, err := store.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Nil(t, session)
	require.NoError(t, s.DeleteDevice(ctx, d.ID))
}

func TestRubikaConversationFiltersPreserveActorsAndAccountIsolation(t *testing.T) {
	ctx := context.Background()
	registry, err := domains.NewProviderRegistry(
		domains.ProviderRegistration{Contract: rubikameow.Contract{Enabled: true}},
		domains.ProviderRegistration{Contract: bale.Contract{}},
		domains.ProviderRegistration{Contract: eitaameow.Contract{Enabled: true}},
	)
	require.NoError(t, err)
	s, st := testService(t, Options{Providers: registry}, nil)
	a, err := st.CreateDevice(ctx, "rubika-a", domains.ProviderRubika)
	require.NoError(t, err)
	b, err := st.CreateDevice(ctx, "rubika-b", domains.ProviderRubika)
	require.NoError(t, err)
	url, secret := "https://synthetic.invalid/events", "test-secret"
	filter := domains.WebhookFilter{PeerTypes: []string{"service"}, SenderIDs: []string{"s0fixture"}, Directions: []string{"incoming"}}
	_, err = s.PatchWebhook(ctx, a.ID, domains.WebhookPatch{URL: &url, Secret: &secret, Filter: &filter})
	require.NoError(t, err)
	for i, peer := range []domains.Peer{{Type: "user", ID: "u0fixture"}, {Type: "service", ID: "s0fixture"}, {Type: "bot", ID: "b0fixture"}} {
		ev := domains.Event{Provider: domains.ProviderRubika, ID: peer.Type, Type: "message", Peer: peer, MessageID: "42", SenderID: peer.ID, Direction: "incoming", Time: time.Now().UTC(), Payload: json.RawMessage(`{"supported":true}`), Message: &domains.Message{ID: "42", ChatID: peer.ID, Kind: "text", Body: "synthetic", Supported: true}}
		require.NoError(t, s.batchSink(a.ConnectionID)(ctx, domains.EventBatch{Events: []domains.Event{ev}, Checkpoints: []domains.CheckpointTransition{{Scope: peer.Key(), Next: "accepted"}}}))
		rows, err := s.EventsFiltered(ctx, a.ID, domains.EventFilter{Peer: peer.Key(), SenderID: peer.ID, Search: "synthetic"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		other, err := s.EventsFiltered(ctx, b.ID, domains.EventFilter{Peer: peer.Key(), SenderID: peer.ID})
		require.NoError(t, err)
		require.Empty(t, other)
		cursor, err := st.ScopedCheckpoint(ctx, a.ConnectionID, peer.Key())
		require.NoError(t, err)
		require.Equal(t, "accepted", cursor)
		if i > 0 {
			require.False(t, (rubikameow.Contract{}).ValidateUserID(peer.ID))
		}
	}
	deliveries, err := st.ListDeliveries(ctx, a.ConnectionID, 100, 0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	var body struct {
		Peer   domains.Peer `json:"peer"`
		Sender string       `json:"sender_id"`
	}
	require.NoError(t, json.Unmarshal(deliveries[0].Body, &body))
	require.Equal(t, "service", body.Peer.Type)
	require.Equal(t, "s0fixture", body.Sender)
	for _, c := range []domains.ProviderContract{bale.Contract{}, eitaameow.Contract{}} {
		require.Error(t, validateFilter(c, filter))
		require.Error(t, c.ValidatePeer(domains.Peer{Type: "service", ID: "s0fixture"}))
	}
}

func TestInvalidProjectionCannotAdvanceCheckpointOrCreateDelivery(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{}, nil)
	d, err := st.CreateDevice(ctx, "projection", domains.ProviderBale)
	require.NoError(t, err)
	event := domains.Event{Provider: domains.ProviderBale, ID: "edit", Type: "message.edited", Peer: domains.Peer{Type: "user", ID: "7"}, MessageID: "42", Payload: json.RawMessage(`{"kind":"text","message":"synthetic"}`), Message: &domains.Message{ID: "42", ChatID: "user:7", Body: "synthetic", Supported: true}}
	accept := func(e domains.Event) error {
		return s.batchSink(d.ConnectionID)(ctx, domains.EventBatch{Events: []domains.Event{e}, Checkpoints: []domains.CheckpointTransition{{Scope: "account", Next: "new"}}})
	}
	codeIs(t, accept(event), "INVALID_MESSAGE_PROJECTION")
	event.Message.ChatID = "7"
	codeIs(t, accept(event), "INVALID_MESSAGE_PROJECTION")
	cursor, err := st.ScopedCheckpoint(ctx, d.ConnectionID, "account")
	require.NoError(t, err)
	require.Empty(t, cursor)
	count, err := st.EventCount(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Zero(t, count)
	event.Message.OriginalMessageID = "42"
	require.NoError(t, accept(event))
	cursor, err = st.ScopedCheckpoint(ctx, d.ConnectionID, "account")
	require.NoError(t, err)
	require.Equal(t, "new", cursor)
}
