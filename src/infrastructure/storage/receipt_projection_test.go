package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestReceiptProjectionCorrectionPreservesPersistedBody(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "receipt-snapshot")
	target := WebhookTarget{URL: "https://synthetic.test/webhook", Secret: "synthetic"}
	// An already accepted body must survive the contract correction unchanged.
	old := domains.Event{ID: "same-native-receipt", Type: "message.read", Peer: domains.Peer{Type: "user", ID: "42"}, Time: time.Unix(1720000000, 0), Checkpoint: "original-checkpoint", Payload: json.RawMessage(`{"start_date":"123","date":"0","range_valid":false,"range_status":"unknown","message_ids_supported":false}`)}
	inserted, err := s.AppendEvent(ctx, d.ConnectionID, old, []WebhookTarget{target})
	require.NoError(t, err)
	require.True(t, inserted)
	jobs, err := s.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	first := jobs[0]
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, first.ID, "delivered", time.Now(), ""))
	require.NoError(t, s.Close())
	reopened, err := Open(path, testKey)
	require.NoError(t, err)
	t.Cleanup(func() { reopened.Close() })

	current := old
	current.Payload = json.RawMessage(`{"start_date":"123","read_date":"0","message_ids_supported":false}`)
	current.Checkpoint = "must-not-replace-checkpoint"
	inserted, err = reopened.AppendEvent(ctx, d.ConnectionID, current, []WebhookTarget{target})
	require.NoError(t, err)
	require.False(t, inserted, "projection changes cannot create another delivery for the same native event")
	checkpoint, err := reopened.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, "original-checkpoint", checkpoint)
	replays, err := reopened.ReplayDelivery(ctx, d.ConnectionID, first.ID, []WebhookTarget{target})
	require.NoError(t, err)
	require.Len(t, replays, 1)
	require.Equal(t, first.EventID, replays[0].EventID)
	require.Equal(t, first.Body, replays[0].Body, "historical signed bytes must not be recomputed")

	current.ID = "new-native-receipt"
	current.Checkpoint = "next-checkpoint"
	inserted, err = reopened.AppendEvent(ctx, d.ConnectionID, current, []WebhookTarget{target})
	require.NoError(t, err)
	require.True(t, inserted, "new receipts use the corrected contract")
	stored, err := reopened.ListEvents(ctx, d.ConnectionID, "", 10, 0)
	require.NoError(t, err)
	require.Len(t, stored, 2)
	for _, ev := range stored {
		if ev.ID == scopedEventID(d.ConnectionID, current) {
			require.JSONEq(t, string(current.Payload), string(ev.Payload))
			return
		}
	}
	t.Fatal("new receipt was not retained")
}
