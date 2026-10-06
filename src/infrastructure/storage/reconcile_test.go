package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func ownEcho(op domains.Operation) domains.Event {
	return domains.Event{ID: "native-echo-" + op.Request.RequestID, Type: "message", Peer: op.Request.Peer,
		MessageID: op.Request.RequestID, SenderID: "456", Direction: "outgoing",
		Time: time.UnixMilli(1720000000000), Payload: json.RawMessage(`{"text":"hello"}`)}
}

func TestOwnEchoReconcilesDurablyWithoutRetry(t *testing.T) {
	for _, state := range []string{"sending", "unknown"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s, path := testStore(t)
			d := device(t, s, "echo")
			require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
			op, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("hello"), "echo-request", 10)
			require.NoError(t, err)
			claimed, err := s.ClaimOperations(ctx, 1)
			require.NoError(t, err)
			require.Len(t, claimed, 1)
			if state == "unknown" {
				require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, state, nil, "SEND_UNKNOWN", "response was lost"))
			}
			echo := ownEcho(op)
			echo.Checkpoint = "after-own-echo"
			inserted, err := s.AppendEvent(ctx, d.ConnectionID, echo, []WebhookTarget{{URL: "https://example.test/hook"}})
			require.NoError(t, err)
			require.True(t, inserted)
			got, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
			require.NoError(t, err)
			require.Equal(t, "succeeded", got.State)
			require.NotNil(t, got.Result)
			require.Equal(t, echo.MessageID, got.Result.MessageID)
			require.Nil(t, got.Result.Data)
			// JSON round-tripping can change the location while preserving the instant.
			require.True(t, echo.Time.Equal(got.Result.Date), "reconciled acceptance time must survive persistence")
			require.Empty(t, got.ErrorCode)
			require.Empty(t, got.ErrorMessage)
			// A late ambiguous RPC result must never downgrade proven acceptance.
			errorCode(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "late timeout"), "OPERATION_CONFLICT")
			inserted, err = s.AppendEvent(ctx, d.ConnectionID, echo, []WebhookTarget{{URL: "https://example.test/hook"}})
			require.NoError(t, err)
			require.False(t, inserted)
			jobs, err := s.ListDeliveries(ctx, d.ConnectionID, 100, 0)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			require.NoError(t, s.Close())
			reopened, err := Open(path, testKey)
			require.NoError(t, err)
			defer reopened.Close()
			got, err = reopened.GetOperation(ctx, d.ConnectionID, op.ID)
			require.NoError(t, err)
			require.Equal(t, "succeeded", got.State)
			claimed, err = reopened.ClaimOperations(ctx, 10)
			require.NoError(t, err)
			require.Empty(t, claimed)
		})
	}
}

func TestOwnEchoRequiresExactTrustedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domains.SendRequest, *domains.Event)
	}{
		{"incoming", func(_ *domains.SendRequest, e *domains.Event) { e.Direction = "incoming" }},
		{"foreign sender", func(_ *domains.SendRequest, e *domains.Event) { e.SenderID = "999" }},
		{"absent sender", func(_ *domains.SendRequest, e *domains.Event) { e.SenderID = "" }},
		{"different peer id", func(_ *domains.SendRequest, e *domains.Event) { e.Peer.ID = "123" }},
		{"different peer type", func(_ *domains.SendRequest, e *domains.Event) { e.Peer.Type = "group" }},
		{"different RID", func(_ *domains.SendRequest, e *domains.Event) { e.MessageID = "999" }},
		{"edit", func(_ *domains.SendRequest, e *domains.Event) { e.Type = "message.edited" }},
		{"accepted without sender proof", func(_ *domains.SendRequest, e *domains.Event) { e.Type = "message.accepted" }},
		{"mutation", func(r *domains.SendRequest, _ *domains.Event) { r.Operation = "group.create" }},
		{"unknown kind", func(r *domains.SendRequest, _ *domains.Event) { r.Kind = "operation" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, _ := testStore(t)
			d := device(t, s, "echo")
			require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
			req := textRequest("hello")
			req.RequestID = "987654321"
			echo := ownEcho(domains.Operation{Request: req})
			tc.mutate(&req, &echo)
			op, _, err := s.Enqueue(ctx, d.ConnectionID, req, "", 10)
			require.NoError(t, err)
			_, err = s.ClaimOperations(ctx, 1)
			require.NoError(t, err)
			require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "lost"))
			_, err = s.AppendEvent(ctx, d.ConnectionID, echo, nil)
			require.NoError(t, err)
			got, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
			require.NoError(t, err)
			require.Equal(t, "unknown", got.State)
		})
	}
}

func TestOwnEchoScopeTerminalStatesAndRollback(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	a, b := device(t, s, "first"), device(t, s, "second")
	for _, d := range []domains.Device{a, b} {
		require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
	}
	op, _, err := s.Enqueue(ctx, a.ConnectionID, textRequest("hello"), "", 10)
	require.NoError(t, err)
	_, err = s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, s.FinishOperation(ctx, a.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "lost"))
	echo := ownEcho(op)
	_, err = s.AppendEvent(ctx, b.ConnectionID, echo, nil)
	require.NoError(t, err)
	got, err := s.GetOperation(ctx, a.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", got.State)
	// Delivery failure rolls back both proof and operation reconciliation.
	echo.Checkpoint = "should-not-commit"
	_, err = s.AppendEvent(ctx, a.ConnectionID, echo, []WebhookTarget{{URL: "file:///bad"}})
	require.Error(t, err)
	got, err = s.GetOperation(ctx, a.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", got.State)
	cp, err := s.Checkpoint(ctx, a.ConnectionID)
	require.NoError(t, err)
	require.Empty(t, cp)

	for _, state := range []string{"queued", "failed", "cancelled"} {
		op, _, err = s.Enqueue(ctx, a.ConnectionID, textRequest(state), "", 10)
		require.NoError(t, err)
		if state != "queued" {
			// Arrange a prior authoritative terminal decision independently.
			_, err = s.db.ExecContext(ctx, `UPDATE operations SET state=? WHERE id=?`, state, op.ID)
			require.NoError(t, err)
		}
		_, err = s.AppendEvent(ctx, a.ConnectionID, ownEcho(op), nil)
		require.NoError(t, err)
		got, err = s.GetOperation(ctx, a.ConnectionID, op.ID)
		require.NoError(t, err)
		require.Equal(t, state, got.State)
	}
}

func TestOwnEchoReconcilesOnlyReviewedMessageProducingOperations(t *testing.T) {
	for _, name := range []string{"send.poll", "send.sticker", "send.contact", "send.location", "send.template", "group.create", "story.add", "miniapp.data", "send.unreviewed"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, _ := testStore(t)
			d := device(t, s, "rich-echo")
			require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
			req := domains.SendRequest{Kind: "operation", Operation: name, Peer: domains.Peer{Type: "channel", ID: "77"}, Payload: json.RawMessage(`{}`)}
			op, _, err := s.Enqueue(ctx, d.ConnectionID, req, "rich-once", 10)
			require.NoError(t, err)
			_, err = s.ClaimOperations(ctx, 1)
			require.NoError(t, err)
			require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "response lost"))
			echo := ownEcho(op)
			// Same RID alone does not establish the destination or authenticated sender.
			for i, change := range []func(*domains.Event){func(e *domains.Event) { e.Peer.ID = "78" }, func(e *domains.Event) { e.SenderID = "999" }, func(e *domains.Event) { e.Direction = "incoming" }, func(e *domains.Event) { e.Peer.Type = "group" }} {
				bad := echo
				bad.ID += string(rune('a' + i))
				change(&bad)
				_, err = s.AppendEvent(ctx, d.ConnectionID, bad, nil)
				require.NoError(t, err)
				got, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
				require.NoError(t, err)
				require.Equal(t, "unknown", got.State)
			}
			_, err = s.AppendEvent(ctx, d.ConnectionID, echo, nil)
			require.NoError(t, err)
			got, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
			require.NoError(t, err)
			if name == "send.poll" || name == "send.sticker" || name == "send.contact" || name == "send.location" || name == "send.template" {
				require.Equal(t, "succeeded", got.State)
				require.Equal(t, echo.MessageID, got.Result.MessageID)
			} else {
				require.Equal(t, "unknown", got.State)
			}
		})
	}
}
