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

func TestProviderSearchReviewedMessageAndCaption(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	for _, provider := range []domains.Provider{domains.ProviderEitaa, domains.ProviderRubika} {
		t.Run(string(provider), func(t *testing.T) {
			d, err := s.CreateDevice(ctx, string(provider), provider)
			require.NoError(t, err)
			other, err := s.CreateDevice(ctx, string(provider)+"-other", provider)
			require.NoError(t, err)
			for i, kind := range []string{"text", "image", "unsupported"} {
				e := domains.Event{Provider: provider, ID: fmt.Sprintf("event-%d", i), Type: "message", Peer: domains.Peer{Type: "user", ID: "u0synthetic"}, SenderID: "u0sender", Direction: "incoming", Time: time.Now().UTC(), Payload: json.RawMessage(`{"kind":"text","text":"native secret","quoted_message":{"text":"quoted secret"}}`)}
				e.Message = &domains.Message{ID: "1", ChatID: e.Peer.Key(), Body: "سلام %_🙂", QuotedBody: "quoted secret", Kind: kind, Supported: kind != "unsupported"}
				if kind == "image" {
					e.Message.Media = &domains.MessageMedia{Type: "image", FileID: "synthetic", Name: "image.png"}
				}
				_, err = s.AppendEvent(ctx, d.ConnectionID, e, nil)
				require.NoError(t, err)
			}
			f := domains.EventFilter{Search: "سلام %_🙂", Peer: "user:u0synthetic", Direction: "incoming", SenderID: "u0sender"}
			rows, err := s.ListEventsFiltered(ctx, d.ConnectionID, f)
			require.NoError(t, err)
			require.Len(t, rows, 2)
			f.MediaOnly = true
			rows, err = s.ListEventsFiltered(ctx, d.ConnectionID, f)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "image", rows[0].Message.Kind)
			rows, err = s.ListEventsFiltered(ctx, other.ConnectionID, f)
			require.NoError(t, err)
			require.Empty(t, rows)
			for _, text := range []string{"quoted secret", "native secret"} {
				rows, err = s.ListEventsFiltered(ctx, d.ConnectionID, domains.EventFilter{Search: text})
				require.NoError(t, err)
				require.Empty(t, rows)
			}
		})
	}
}
