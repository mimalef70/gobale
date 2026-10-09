package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func TestMessageProjectionSnapshotSurvivesDuplicateRestartAndReplay(t *testing.T) {
	for _, original := range []struct{ name, status string }{{"First name", "available"}, {"", "unavailable"}} {
		t.Run(original.status, func(t *testing.T) {
			ctx := context.Background()
			s, path := testStore(t)
			d := device(t, s, "message-snapshot")
			target := WebhookTarget{URL: "https://synthetic.test/webhook", Secret: "synthetic"}
			ev := event("message-snapshot", "checkpoint-one")
			ev.MessageID = "-9007199254740993"
			ev.SenderID = "42"
			ev.Message = &domains.Message{ID: ev.MessageID, ChatID: ev.Peer.ID, From: "42", SenderDisplayName: original.name, SenderNameStatus: original.status, IsFromMe: ptr(false), Timestamp: ev.Time, Kind: "text", Supported: true, Body: "synthetic"}
			inserted, err := s.AppendEvent(ctx, d.ConnectionID, ev, []WebhookTarget{target})
			require.NoError(t, err)
			require.True(t, inserted)
			jobs, err := s.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			first := jobs[0]
			var envelope map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(first.Body, &envelope))
			require.NotContains(t, envelope, "message")
			require.Contains(t, envelope, "content")
			var projection domains.Message
			require.NoError(t, json.Unmarshal(envelope["payload"], &projection))
			require.Equal(t, original.name, projection.SenderDisplayName)
			ev.Message.SenderDisplayName = "Changed cached name"
			ev.Message.SenderNameStatus = "available"
			ev.Checkpoint = "checkpoint-two"
			inserted, err = s.AppendEvent(ctx, d.ConnectionID, ev, []WebhookTarget{target})
			require.NoError(t, err)
			require.False(t, inserted)
			require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, first.ID, "delivered", time.Now(), ""))
			require.NoError(t, s.Close())
			reopened, err := Open(path, testKey)
			require.NoError(t, err)
			t.Cleanup(func() { reopened.Close() })
			replayed, err := reopened.ReplayDelivery(ctx, d.ConnectionID, first.ID, []WebhookTarget{target})
			require.NoError(t, err)
			require.Len(t, replayed, 1)
			require.Equal(t, first.Body, replayed[0].Body, "replay must not recompute display names or projections")
			var body domains.Event
			require.NoError(t, json.Unmarshal(replayed[0].Body, &body))
			require.Equal(t, original.name, body.Message.SenderDisplayName)
			require.Equal(t, original.status, body.Message.SenderNameStatus)
			require.Equal(t, "-9007199254740993", body.Message.ID)
		})
	}
}

func TestHistoricalMessageEnvelopePreservesReplayAndSearch(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "historical-message")
	target := WebhookTarget{URL: "https://synthetic.test/webhook", Secret: "synthetic"}
	ev := event("source-event", "checkpoint-before")
	ev.Payload = json.RawMessage(`{"kind":"text","message":"original 🙂","quoted_message":{"content":{"message":"quoted secret"}}}`)
	ev.Message = &domains.Message{ID: "123", Body: "original 🙂", Kind: "text", Supported: true, SenderNameStatus: "unavailable"}
	inserted, err := s.AppendEvent(ctx, d.ConnectionID, ev, []WebhookTarget{target})
	require.NoError(t, err)
	require.True(t, inserted)
	// Recreate the actual pre-upgrade storage/wire representation, bypassing
	// current serialization. No runtime migration may rewrite these bytes.
	stored, err := s.ListEvents(ctx, d.ConnectionID, "", 10, 0)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	type historicalRecord domains.Event
	oldBody, err := json.Marshal(historicalRecord(stored[0]))
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE events SET body=? WHERE connection_id=?`, string(oldBody), d.ConnectionID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE deliveries SET body=? WHERE connection_id=?`, string(oldBody), d.ConnectionID)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = Open(path, testKey)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ev.Message.SenderDisplayName = "newly resolved name"
	inserted, err = s.AppendEvent(ctx, d.ConnectionID, ev, []WebhookTarget{target})
	require.NoError(t, err)
	require.False(t, inserted)
	jobs, err := s.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, string(oldBody), string(jobs[0].Body))
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, jobs[0].ID, "delivered", time.Now(), ""))
	replayed, err := s.ReplayDelivery(ctx, d.ConnectionID, jobs[0].ID, []WebhookTarget{target})
	require.NoError(t, err)
	require.Equal(t, string(oldBody), string(replayed[0].Body))
	ev.ID = "new-source-event"
	_, err = s.AppendEvent(ctx, d.ConnectionID, ev, nil)
	require.NoError(t, err)
	for _, query := range []struct {
		text  string
		count int
	}{{"original 🙂", 2}, {"quoted secret", 0}} {
		found, err := s.ListEventsFiltered(ctx, d.ConnectionID, domains.EventFilter{Search: query.text, Limit: 10})
		require.NoError(t, err)
		require.Len(t, found, query.count)
		for _, got := range found {
			require.Equal(t, "original 🙂", got.Message.Body)
		}
	}
}
