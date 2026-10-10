package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func providerDevice(t *testing.T, s *Store, alias string, provider domains.Provider) domains.Device {
	t.Helper()
	d, err := s.CreateDevice(context.Background(), alias, provider)
	require.NoError(t, err)
	return d
}

func TestProviderBatchesAcceptDuplicateAndEmptyPagesWithIndependentCursors(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := providerDevice(t, s, "eitaa", domains.ProviderEitaa)
	other := providerDevice(t, s, "rubika", domains.ProviderRubika)
	ev := event("same-native-id", "")
	ev.Peer.ID = "opaque-guid"
	ev.SenderID, ev.Direction = "sender-guid", "incoming"
	ev.Payload = json.RawMessage(`{"kind":"text","message":"synthetic unicode پیام"}`)
	targets := []WebhookTarget{{URL: "https://synthetic.invalid/message", Secret: "secret", Events: []string{"message"}}, {URL: "https://synthetic.invalid/edit", Secret: "secret", Events: []string{"message.edited"}}}
	batch := domains.EventBatch{Events: []domains.Event{ev}, Checkpoints: []domains.CheckpointTransition{{Scope: "account", Next: "pts-1"}, {Scope: "channel:alpha", Next: "pts-5"}}}
	created, err := s.AppendBatch(ctx, d.ConnectionID, batch, targets)
	require.NoError(t, err)
	require.Equal(t, 1, created)
	batch.Checkpoints = []domains.CheckpointTransition{{Scope: "account", Expected: "pts-1", Next: "pts-2"}}
	created, err = s.AppendBatch(ctx, d.ConnectionID, batch, targets)
	require.NoError(t, err)
	require.Zero(t, created)
	empty := domains.EventBatch{Checkpoints: []domains.CheckpointTransition{{Scope: "channel:alpha", Expected: "pts-5", Next: "pts-6"}}}
	created, err = s.AppendBatch(ctx, d.ConnectionID, empty, targets)
	require.NoError(t, err)
	require.Zero(t, created)
	for scope, want := range map[string]string{"account": "pts-2", "channel:alpha": "pts-6"} {
		got, err := s.ScopedCheckpoint(ctx, d.ConnectionID, scope)
		require.NoError(t, err)
		require.Equal(t, want, got)
		got, err = s.ScopedCheckpoint(ctx, other.ConnectionID, scope)
		require.NoError(t, err)
		require.Empty(t, got)
	}
	created, err = s.AppendBatch(ctx, other.ConnectionID, domains.EventBatch{Events: []domains.Event{ev}}, targets)
	require.NoError(t, err)
	require.Equal(t, 1, created)
	for _, connection := range []domains.Device{d, other} {
		events, err := s.ListEventsFiltered(ctx, connection.ConnectionID, domains.EventFilter{Direction: "incoming"})
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.Equal(t, connection.Provider, events[0].Provider)
		deliveries, err := s.ListDeliveries(ctx, connection.ConnectionID, 10, 0)
		require.NoError(t, err)
		require.Len(t, deliveries, 1)
		require.Equal(t, targets[0].URL, deliveries[0].URL)
	}
	// Stale pages cannot partially append an event or rewind another cursor.
	ev.ID = "stale-page-event"
	created, err = s.AppendBatch(ctx, d.ConnectionID, domains.EventBatch{Events: []domains.Event{ev}, Checkpoints: []domains.CheckpointTransition{{Scope: "account", Expected: "pts-1", Next: "pts-3"}, {Scope: "channel:alpha", Expected: "pts-6", Next: "pts-7"}}}, targets)
	errorCode(t, err, "CHECKPOINT_CONFLICT")
	require.Zero(t, created)
	count, err := s.EventCount(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	cursor, err := s.ScopedCheckpoint(ctx, d.ConnectionID, "channel:alpha")
	require.NoError(t, err)
	require.Equal(t, "pts-6", cursor)
}

func TestProviderBatchRollbackIncludesEventsMediaProofAndCheckpoints(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "bale")
	require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
	op, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("ambiguous"), "once", AdmissionLimits{Global: 20})
	require.NoError(t, err)
	_, err = s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "synthetic"))
	m := privateMedia()
	echo := ownEcho(op)
	echo.Media = &m
	echo.Payload = json.RawMessage(`{"kind":"document","file_id":"777"}`)
	batch := domains.EventBatch{Events: []domains.Event{echo}, Proofs: []domains.AcceptanceProof{{Provider: domains.ProviderBale, Kind: domains.BaleMessageEchoProof, EventID: echo.ID, RequestID: op.Request.RequestID}}, Checkpoints: []domains.CheckpointTransition{{Scope: "account", Next: "accepted"}}}
	_, err = s.db.Exec(`CREATE TRIGGER synthetic_checkpoint_failure BEFORE INSERT ON provider_checkpoints BEGIN SELECT RAISE(ABORT,'synthetic storage failure'); END`)
	require.NoError(t, err)
	created, err := s.AppendBatch(ctx, d.ConnectionID, batch, []WebhookTarget{{URL: "https://synthetic.invalid/hook", Secret: "synthetic"}})
	require.Error(t, err)
	require.Zero(t, created)
	for _, table := range []string{"events", "deliveries", "provider_media", "provider_checkpoints"} {
		var count int
		require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table)
	}
	current, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", current.State)
	_, err = s.db.Exec(`DROP TRIGGER synthetic_checkpoint_failure`)
	require.NoError(t, err)
	created, err = s.AppendBatch(ctx, d.ConnectionID, batch, nil)
	require.NoError(t, err)
	require.Equal(t, 1, created)
	current, err = s.GetOperation(ctx, d.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", current.State)
}

func TestProviderProofCannotResolveAnotherProtocolOrChangedDuplicate(t *testing.T) {
	ctx := context.Background()
	for _, provider := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
		t.Run(string(provider), func(t *testing.T) {
			s, _ := testStore(t)
			d := providerDevice(t, s, "account", provider)
			require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
			op, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("ambiguous"), "once", AdmissionLimits{Global: 20})
			require.NoError(t, err)
			_, err = s.ClaimOperations(ctx, 1)
			require.NoError(t, err)
			require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "synthetic"))
			echo := ownEcho(op)
			bad := echo
			bad.Direction = "incoming"
			_, err = s.AppendBatch(ctx, d.ConnectionID, domains.EventBatch{Events: []domains.Event{bad}}, nil)
			require.NoError(t, err)
			proof := domains.AcceptanceProof{Provider: domains.ProviderBale, Kind: domains.BaleMessageEchoProof, EventID: echo.ID, RequestID: op.Request.RequestID}
			_, err = s.AppendBatch(ctx, d.ConnectionID, domains.EventBatch{Events: []domains.Event{echo}, Proofs: []domains.AcceptanceProof{proof}}, nil)
			if provider == domains.ProviderBale {
				require.NoError(t, err)
			} else {
				errorCode(t, err, "INVALID_ACCEPTANCE_PROOF")
				// The older single-event ingress is guarded too.
				echo.ID = "new-echo"
				_, err = s.AppendEvent(ctx, d.ConnectionID, echo, nil)
				require.NoError(t, err)
			}
			current, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
			require.NoError(t, err)
			require.Equal(t, "unknown", current.State)
		})
	}
}

func TestProviderPrivateDataAndIdentityAreBoundToConnection(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	bale := device(t, s, "bale")
	rubika := providerDevice(t, s, "rubika", domains.ProviderRubika)
	_, err := s.db.Exec(`UPDATE devices SET provider='rubika' WHERE connection_id=?`, bale.ConnectionID)
	require.ErrorContains(t, err, "immutable")
	_, err = s.CreateDevice(ctx, "missing-provider", "")
	errorCode(t, err, "INVALID_PROVIDER")
	wrong := &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "same-account", Token: "synthetic-secret"}
	errorCode(t, s.SaveSession(ctx, rubika.ConnectionID, wrong), "PROVIDER_MISMATCH")
	session := &domains.Session{Provider: domains.ProviderRubika, Version: 1, UserID: "same-account", Data: json.RawMessage(`{"rsa_key":"synthetic-private-key"}`)}
	require.NoError(t, s.SaveSession(ctx, rubika.ConnectionID, session))
	loaded, err := s.LoadSession(ctx, rubika.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, session, loaded)
	peer := domains.Peer{Type: "user", ID: "u0synthetic"}
	m := domains.ProviderMedia{Provider: domains.ProviderRubika, Version: 1, FileID: "file-guid", Data: json.RawMessage(`{"token":"synthetic-access-secret"}`), Size: 12, Name: "synthetic.bin", ContentType: "application/octet-stream"}
	available, err := s.SaveProviderMedia(ctx, rubika.ConnectionID, peer, "message-guid", m)
	require.NoError(t, err)
	require.True(t, available)
	media, err := s.GetProviderMedia(ctx, rubika.ConnectionID, peer, "message-guid")
	require.NoError(t, err)
	require.Equal(t, m, media)
	raw, err := json.Marshal(media)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "synthetic-access-secret")
	m.Provider = domains.ProviderEitaa
	_, err = s.SaveProviderMedia(ctx, rubika.ConnectionID, peer, "message-guid", m)
	errorCode(t, err, "PROVIDER_MISMATCH")
	// Even a database row copied under a different connection cannot decrypt.
	require.NoError(t, s.BindAccount(ctx, bale.ConnectionID, "same-account"))
	_, err = s.db.Exec(`INSERT INTO sessions(connection_id,cipher) SELECT ?,cipher FROM sessions WHERE connection_id=?`, bale.ConnectionID, rubika.ConnectionID)
	require.NoError(t, err)
	_, err = s.LoadSession(ctx, bale.ConnectionID)
	require.Error(t, err)
}

func TestProviderSendSharesPreventHungProviderStarvation(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	s.SetSendConcurrency(4)
	devices := map[domains.Provider][]domains.Device{}
	for _, provider := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
		for i := 0; i < 3; i++ {
			d := providerDevice(t, s, fmt.Sprintf("%s-%d", provider, i), provider)
			devices[provider] = append(devices[provider], d)
			_, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("synthetic"), "once", AdmissionLimits{Global: 20})
			require.NoError(t, err)
		}
	}
	claimed, err := s.ClaimOperations(ctx, 4)
	require.NoError(t, err)
	require.Len(t, claimed, 3)
	counts := map[domains.Provider]int{}
	var healthy domains.Operation
	for _, op := range claimed {
		counts[op.Provider]++
		if op.Provider == domains.ProviderBale {
			healthy = op
		}
	}
	for _, count := range counts {
		require.Equal(t, 1, count)
	}
	// Eitaa and Rubika remain in sending. Bale continues across its accounts.
	for i := 0; i < 2; i++ {
		require.NoError(t, s.FinishOperation(ctx, healthy.ConnectionID, healthy.ID, "succeeded", &domains.SendResult{MessageID: fmt.Sprint(i), Date: time.Now()}, "", ""))
		claimed, err = s.ClaimOperations(ctx, 4)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.Equal(t, domains.ProviderBale, claimed[0].Provider)
		require.NotEqual(t, healthy.ConnectionID, claimed[0].ConnectionID)
		healthy = claimed[0]
	}
}

func TestSessionRotationCannotRecreateLoggedOutOrDeletedSessions(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := providerDevice(t, s, "rotating", domains.ProviderRubika)
	session := &domains.Session{Provider: domains.ProviderRubika, Version: 1, UserID: "u0sameaccount", Data: json.RawMessage(`{"key":"initial-synthetic-key"}`)}
	errorCode(t, s.UpdateSession(ctx, d.ConnectionID, session), "SESSION_NOT_FOUND")
	unbound, err := s.GetDevice(ctx, d.ID)
	require.NoError(t, err)
	require.Empty(t, unbound.AccountID, "a failed rotation must not bind the account")
	require.NoError(t, s.SaveSession(ctx, d.ConnectionID, session))
	session.Data = json.RawMessage(`{"key":"rotated-synthetic-key"}`)
	require.NoError(t, s.UpdateSession(ctx, d.ConnectionID, session))
	loaded, err := s.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, session, loaded)
	wrong := *session
	wrong.Provider = domains.ProviderEitaa
	errorCode(t, s.UpdateSession(ctx, d.ConnectionID, &wrong), "PROVIDER_MISMATCH")
	wrong = *session
	wrong.UserID = "u0otheraccount"
	errorCode(t, s.UpdateSession(ctx, d.ConnectionID, &wrong), "ACCOUNT_CONFLICT")
	require.NoError(t, s.LogoutConnection(ctx, d.ConnectionID))
	errorCode(t, s.UpdateSession(ctx, d.ConnectionID, session), "SESSION_NOT_FOUND")
	loaded, err = s.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Nil(t, loaded)
	require.NoError(t, s.DeleteDevice(ctx, d.ConnectionID))
	errorCode(t, s.UpdateSession(ctx, d.ConnectionID, session), "NOT_FOUND")
	replacement := providerDevice(t, s, d.ID, domains.ProviderRubika)
	loaded, err = s.LoadSession(ctx, replacement.ConnectionID)
	require.NoError(t, err)
	require.Nil(t, loaded)
}
