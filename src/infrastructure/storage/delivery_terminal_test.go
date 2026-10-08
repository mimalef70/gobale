package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func TestTerminalReceiptFailureSurvivesRestartAndReplayFollowsQueuedMessages(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "terminal-receipt")
	target := WebhookTarget{URL: "https://one.test/hook", Secret: "synthetic"}
	other := WebhookTarget{URL: "https://two.test/hook", Secret: "synthetic-other"}
	receipt := domains.Event{ID: "receipt", Type: "message.read", Peer: domains.Peer{Type: "user", ID: "42"}, Time: time.Unix(1720000000, 0), Payload: json.RawMessage(`{"start_date":"123","date":"0","range_valid":false,"range_status":"unknown","message_ids_supported":false}`)}
	_, err := s.AppendEvent(ctx, d.ConnectionID, receipt, []WebhookTarget{target, other})
	require.NoError(t, err)
	_, err = s.AppendEvent(ctx, d.ConnectionID, event("later-message", ""), []WebhookTarget{target, other})
	require.NoError(t, err)
	jobs, err := s.ClaimDeliveries(ctx, 10, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	var failed domains.Delivery
	for _, job := range jobs {
		if job.URL == target.URL {
			failed = job
			require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, job.ID, "failed", time.Time{}, "webhook endpoint permanently rejected delivery (HTTP 422); explicit retry or replay required"))
		} else {
			require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, job.ID, "delivered", time.Time{}, ""))
		}
	}
	require.NotEmpty(t, failed.ID)
	require.NoError(t, s.Close())
	reopened, err := Open(path, testKey)
	require.NoError(t, err)
	t.Cleanup(func() { reopened.Close() })
	retained, err := reopened.GetDelivery(ctx, d.ConnectionID, failed.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", retained.State)
	require.Equal(t, 1, retained.Attempts)
	require.Contains(t, retained.LastError, "HTTP 422")
	require.Equal(t, failed.Body, retained.Body)

	replays, err := reopened.ReplayDelivery(ctx, d.ConnectionID, failed.ID, []WebhookTarget{target})
	require.NoError(t, err)
	require.Len(t, replays, 1)
	require.NotEqual(t, failed.ID, replays[0].ID)
	require.Equal(t, failed.EventID, replays[0].EventID)
	require.Equal(t, failed.Body, replays[0].Body)

	jobs, err = reopened.ClaimDeliveries(ctx, 10, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, jobs, 2, "both destinations advance to the message after terminal receipt handling")
	for _, job := range jobs {
		require.Equal(t, scopedEventID(d.ConnectionID, event("later-message", "")), job.EventID)
		require.NoError(t, reopened.UpdateDelivery(ctx, d.ConnectionID, job.ID, "delivered", time.Time{}, ""))
	}
	jobs, err = reopened.ClaimDeliveries(ctx, 10, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, replays[0].ID, jobs[0].ID, "replay is new work after already queued messages")
	retained, err = reopened.GetDelivery(ctx, d.ConnectionID, failed.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", retained.State, "replay retains the terminal audit entry")
}
