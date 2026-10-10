package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestHistoryMediaAvailabilityRequiresExactCurrentReference(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "available")
	peer := domains.Peer{Type: "user", ID: "42"}
	original := privateMedia()
	for range 2 {
		available, err := s.SaveProviderMedia(ctx, d.ConnectionID, peer, "123", original)
		require.NoError(t, err)
		require.True(t, available, "an identical existing reference remains available")
	}
	newer := original
	newer.FileID = "888"
	newer.AccessHash = "999"
	edited := documentEvent("edit", 2000, &newer)
	edited.Type = "message.edited"
	_, err := s.AppendEvent(ctx, d.ConnectionID, edited, nil)
	require.NoError(t, err)
	available, err := s.SaveProviderMedia(ctx, d.ConnectionID, peer, "123", original)
	require.NoError(t, err)
	require.False(t, available, "history cannot advertise the former attachment")
	available, err = s.SaveProviderMedia(ctx, d.ConnectionID, peer, "123", newer)
	require.NoError(t, err)
	require.True(t, available, "the current matching attachment remains available")
	wrongHash := newer
	wrongHash.AccessHash = "1000"
	available, err = s.SaveProviderMedia(ctx, d.ConnectionID, peer, "123", wrongHash)
	require.NoError(t, err)
	require.False(t, available, "file ID alone cannot establish the matching private reference")
	deleted := documentEvent("delete", 3000, nil)
	deleted.Type = "message.deleted"
	_, err = s.AppendEvent(ctx, d.ConnectionID, deleted, nil)
	require.NoError(t, err)
	available, err = s.SaveProviderMedia(ctx, d.ConnectionID, peer, "123", newer)
	require.NoError(t, err)
	require.False(t, available, "history cannot advertise a tombstoned attachment")
}

func TestLateMediaEventPublishesTruthfulAvailabilityAtomically(t *testing.T) {
	for _, newerKind := range []string{"none", "same", "different", "deleted"} {
		t.Run(newerKind, func(t *testing.T) {
			s, _ := testStore(t)
			ctx := context.Background()
			d := device(t, s, "late")
			original := privateMedia()
			if newerKind != "none" {
				newer := original
				if newerKind == "different" {
					newer.FileID = "888"
					newer.AccessHash = "999"
				}
				prior := documentEvent("newer", 2000, &newer)
				prior.Type = "message.edited"
				if newerKind == "deleted" {
					prior.Type = "message.deleted"
					prior.Media = nil
				}
				_, err := s.AppendEvent(ctx, d.ConnectionID, prior, nil)
				require.NoError(t, err)
			}
			late := documentEvent("old-original", 1000, &original)
			late.Payload = json.RawMessage(`{"kind":"template","content":{"kind":"document","file_id":"777","name":"sample.jpg","download_supported":false},"quoted_message":{"content":{"kind":"document","file_id":"other","download_supported":false}},"template_id":"9223372036854775807"}`)
			late.Message = &domains.Message{ID: "123", Media: &domains.MessageMedia{FileID: "777", Name: "sample.jpg"}}
			target := WebhookTarget{URL: "https://example.test/events", Secret: "synthetic"}
			inserted, err := s.AppendEvent(ctx, d.ConnectionID, late, []WebhookTarget{target})
			require.NoError(t, err)
			require.True(t, inserted)
			require.False(t, late.Message.Media.DownloadSupported, "normalization must not mutate caller-owned pointers")
			deliveries, err := s.ListDeliveries(ctx, d.ConnectionID, 10, 0)
			require.NoError(t, err)
			require.Len(t, deliveries, 1)
			var published domains.Event
			require.NoError(t, json.Unmarshal(deliveries[0].Body, &published))
			want := newerKind == "none" || newerKind == "same"
			require.Equal(t, want, published.Message.Media.DownloadSupported)
			require.Equal(t, "777", published.Message.Media.FileID)
			require.Equal(t, "sample.jpg", published.Message.Media.Name)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(published.Payload, &payload))
			require.Equal(t, want, payload["content"].(map[string]any)["download_supported"])
			require.Equal(t, false, payload["quoted_message"].(map[string]any)["content"].(map[string]any)["download_supported"])
			require.Equal(t, "9223372036854775807", payload["template_id"])
			events, err := s.ListEvents(ctx, d.ConnectionID, "", 10, 0)
			require.NoError(t, err)
			found := false
			for _, event := range events {
				if event.ID == published.ID {
					found = true
					require.Equal(t, published.Payload, event.Payload)
					require.Equal(t, published.Message, event.Message)
				}
			}
			require.True(t, found)
			cp, err := s.Checkpoint(ctx, d.ConnectionID)
			require.NoError(t, err)
			require.Equal(t, late.Checkpoint, cp)
			replays, err := s.ReplayDelivery(ctx, d.ConnectionID, deliveries[0].ID, []WebhookTarget{target})
			require.NoError(t, err)
			require.Equal(t, deliveries[0].Body, replays[0].Body)
			// A duplicate never changes its durable availability or checkpoint.
			late.Time = time.UnixMilli(9999)
			late.Checkpoint = "duplicate"
			inserted, err = s.AppendEvent(ctx, d.ConnectionID, late, nil)
			require.NoError(t, err)
			require.False(t, inserted)
			cp, err = s.Checkpoint(ctx, d.ConnectionID)
			require.NoError(t, err)
			require.Equal(t, "cp-old-original", cp)
		})
	}
}
