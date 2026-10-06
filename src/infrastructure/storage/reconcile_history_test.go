package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

// Arrange the durable state produced by the older missing-voice allowlist.
// Persist the echo while queued so the fixed reconciliation cannot consume it,
// then transition through the normal claim/unknown paths without any provider.
func legacyUnknownVoice(t *testing.T, s *Store, d domains.Device, name string, mutate func(*domains.Event), proofConnection string) (domains.Operation, domains.Event) {
	t.Helper()
	ctx := context.Background()
	req := domains.SendRequest{Kind: "voice", Peer: domains.Peer{Type: "user", ID: "123"}}
	op, _, err := s.Enqueue(ctx, d.ConnectionID, req, name, 1000)
	require.NoError(t, err)
	echo := ownEcho(op)
	echo.Payload = json.RawMessage(`{"kind":"document","media_type":"voice","mime_type":"audio/ogg","duration":1000}`)
	echo.Checkpoint = "durable-" + name
	if mutate != nil {
		mutate(&echo)
	}
	if proofConnection == "" {
		proofConnection = d.ConnectionID
	}
	_, err = s.AppendEvent(ctx, proofConnection, echo, []WebhookTarget{{URL: "https://example.test/hook"}})
	require.NoError(t, err)
	claimed, err := s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, op.ID, claimed[0].ID)
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "response lost"))
	return op, echo
}

func TestStoredVoiceProofsReconcileOnUpgradeInBoundedBatches(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "legacy-voice")
	require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
	var ops []domains.Operation
	for i := range 102 {
		op, _ := legacyUnknownVoice(t, s, d, fmt.Sprintf("legacy-%d", i), nil, "")
		ops = append(ops, op)
	}
	// Startup first marks interrupted writes unknown, then applies durable proof.
	_, err := s.db.Exec(`UPDATE operations SET state='sending' WHERE id=?`, ops[0].ID)
	require.NoError(t, err)
	checkpoint, err := s.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, err)
	restoreLegacyDeliveryOrder(t, s)
	_, err = s.db.Exec(`UPDATE gobale_meta SET version=4`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	upgraded, err := Open(path, testKey)
	require.NoError(t, err)
	defer upgraded.Close()
	for _, op := range ops {
		got, err := upgraded.GetOperation(ctx, d.ConnectionID, op.ID)
		require.NoError(t, err)
		require.Equal(t, "succeeded", got.State)
		require.Equal(t, op.Request.RequestID, got.Result.MessageID)
		require.Empty(t, got.ErrorCode)
	}
	count, err := upgraded.EventCount(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, 102, count)
	deliveries, err := upgraded.ListDeliveries(ctx, d.ConnectionID, 500, 0)
	require.NoError(t, err)
	require.Len(t, deliveries, 102)
	for _, delivery := range deliveries {
		require.Equal(t, "queued", delivery.State)
		require.Zero(t, delivery.Attempts)
	}
	after, err := upgraded.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, checkpoint, after)
	claimed, err := upgraded.ClaimOperations(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, claimed, "restoring proof must never retransmit an accepted voice")
	require.NoError(t, upgraded.reconcileStoredVoiceProofs(ctx), "repeated startup repair must be idempotent")
}

func TestStoredVoiceProofsRejectForeignIncompleteAndTerminalEvidence(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "legacy-voice")
	other := device(t, s, "other")
	for _, conn := range []string{d.ConnectionID, other.ConnectionID} {
		require.NoError(t, s.BindAccount(ctx, conn, "456"))
	}
	var ops []domains.Operation
	for i, mutate := range []func(*domains.Event){
		func(e *domains.Event) { e.SenderID = "999" },
		func(e *domains.Event) { e.Peer.ID = "321" },
		func(e *domains.Event) { e.Peer.Type = "group" },
		func(e *domains.Event) { e.MessageID = "999" },
		func(e *domains.Event) { e.Direction = "incoming" },
		func(e *domains.Event) { e.Type = "message.accepted" },
		func(e *domains.Event) { e.Time = time.UnixMilli(0) },
	} {
		op, _ := legacyUnknownVoice(t, s, d, fmt.Sprintf("invalid-%d", i), mutate, "")
		ops = append(ops, op)
	}
	foreign, _ := legacyUnknownVoice(t, s, d, "foreign-connection", nil, other.ConnectionID)
	ops = append(ops, foreign)
	wrongAccount, wrongAccountEcho := legacyUnknownVoice(t, s, d, "foreign-account", nil, "")
	_, err := s.db.Exec(`UPDATE events SET body=json_set(body,'$.device_id','999') WHERE connection_id=? AND id=?`, d.ConnectionID, scopedEventID(d.ConnectionID, wrongAccountEcho))
	require.NoError(t, err)
	ops = append(ops, wrongAccount)
	terminal, _ := legacyUnknownVoice(t, s, d, "terminal", nil, "")
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, terminal.ID, "failed", nil, "AUTHORITATIVE_FAILURE", "synthetic terminal decision"))
	require.NoError(t, s.Close())
	reopened, err := Open(path, testKey)
	require.NoError(t, err)
	defer reopened.Close()
	for _, op := range ops {
		got, err := reopened.GetOperation(ctx, d.ConnectionID, op.ID)
		require.NoError(t, err)
		require.Equal(t, "unknown", got.State)
	}
	got, err := reopened.GetOperation(ctx, d.ConnectionID, terminal.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.State)
}

func TestDuplicateVoiceEchoUsesStoredProofOnlyAndDoesNotAdvanceCheckpoint(t *testing.T) {
	for _, originalValid := range []bool{true, false} {
		t.Run(fmt.Sprint(originalValid), func(t *testing.T) {
			ctx := context.Background()
			s, _ := testStore(t)
			d := device(t, s, "duplicate")
			require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
			op, original := legacyUnknownVoice(t, s, d, "original", func(e *domains.Event) {
				if !originalValid {
					e.SenderID = "999"
				}
			}, "")
			duplicate := original
			duplicate.Checkpoint = "must-not-advance"
			// Invert caller proof: stored evidence must win in both directions.
			duplicate.SenderID = "456"
			if originalValid {
				duplicate.SenderID = "999"
			}
			inserted, err := s.AppendEvent(ctx, d.ConnectionID, duplicate, []WebhookTarget{{URL: "https://new-target.test/hook"}})
			require.NoError(t, err)
			require.False(t, inserted)
			got, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
			require.NoError(t, err)
			if originalValid {
				require.Equal(t, "succeeded", got.State)
			} else {
				require.Equal(t, "unknown", got.State)
			}
			checkpoint, err := s.Checkpoint(ctx, d.ConnectionID)
			require.NoError(t, err)
			require.Equal(t, original.Checkpoint, checkpoint)
			deliveries, err := s.ListDeliveries(ctx, d.ConnectionID, 100, 0)
			require.NoError(t, err)
			require.Len(t, deliveries, 1)
			require.Equal(t, "https://example.test/hook", deliveries[0].URL)
		})
	}
}

func TestStoredVoiceProofRepairRollsBackOnPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "rollback")
	require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "456"))
	a, _ := legacyUnknownVoice(t, s, d, "A", nil, "")
	b, _ := legacyUnknownVoice(t, s, d, "B", nil, "")
	_, err := s.db.Exec(`CREATE TRIGGER fail_second_proof BEFORE UPDATE OF state ON operations WHEN NEW.state='succeeded' AND NEW.id='` + b.ID + `' BEGIN SELECT RAISE(ABORT,'synthetic persistence failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, s.reconcileStoredVoiceProofs(ctx), "synthetic persistence failure")
	for _, op := range []domains.Operation{a, b} {
		got, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
		require.NoError(t, err)
		require.Equal(t, "unknown", got.State, "repair must not partially commit")
	}
	checkpoint, err := s.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, "durable-B", checkpoint)
	_, err = s.db.Exec(`DROP TRIGGER fail_second_proof`)
	require.NoError(t, err)
	require.NoError(t, s.reconcileStoredVoiceProofs(ctx))
}
