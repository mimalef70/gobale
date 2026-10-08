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
