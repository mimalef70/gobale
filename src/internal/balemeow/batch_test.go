package balemeow

import (
	"context"
	"errors"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRecoveryBatchPersistsEventsAndCursorTogether(t *testing.T) {
	c := New(Options{})
	cp := recoveryCheckpoint{Version: 1, Account: "42", Routes: map[string]int32{"1": 2}}
	previous := `{"version":1,"account":"42","routes":{"1":1}}`
	conn := &connection{account: "42", checkpoint: cp, checkpointRaw: previous}
	failure := errors.New("synthetic storage failure")
	calls := 0
	conn.batchSink = func(_ context.Context, b domains.EventBatch) error {
		calls++
		require.Len(t, b.Events, 2)
		require.Len(t, b.Checkpoints, 1)
		require.Equal(t, previous, b.Checkpoints[0].Expected)
		require.JSONEq(t, `{"version":1,"account":"42","routes":{"1":2}}`, b.Checkpoints[0].Next)
		require.Empty(t, b.Events[1].Checkpoint)
		if calls == 1 {
			return failure
		}
		return nil
	}
	events := []domains.Event{{ID: "event-one", Type: "message", AccountID: "42", Time: time.Now()}}
	require.ErrorIs(t, c.persistRecovery(context.Background(), conn, events, "catchup"), failure)
	require.Equal(t, previous, conn.checkpointRaw)
	require.NoError(t, c.persistRecovery(context.Background(), conn, events, "catchup"))
	require.NotEqual(t, previous, conn.checkpointRaw)
}
func TestNativeBatchOnlyProvesOwnMessageRID(t *testing.T) {
	c := New(Options{})
	accept := c.prepareBatchSink(func(_ context.Context, b domains.EventBatch) error {
		require.Len(t, b.Proofs, 1)
		require.Equal(t, "123", b.Proofs[0].RequestID)
		require.Equal(t, domains.ProviderBale, b.Events[0].Provider)
		return nil
	})
	require.NoError(t, accept(context.Background(), domains.EventBatch{Events: []domains.Event{
		{ID: "own", Type: "message", AccountID: "42", SenderID: "42", Direction: "outgoing", MessageID: "123"},
		{ID: "incoming", Type: "message", AccountID: "42", SenderID: "43", Direction: "incoming", MessageID: "124"},
		{ID: "receipt", Type: "message.receipt", AccountID: "42", SenderID: "42", Direction: "outgoing", MessageID: "125"},
	}}))
}
