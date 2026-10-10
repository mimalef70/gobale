package providers_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/mimalef70/goomni/src/internal/eitaameow"
	"github.com/mimalef70/goomni/src/internal/rubikameow"
	"github.com/stretchr/testify/require"
)

// Discovery limits must describe admission, including non-ASCII text. This is
// deliberately tested against validation rather than just a descriptor snapshot.
func TestProviderSendDiscoveryMatchesAdmission(t *testing.T) {
	for _, tc := range []struct {
		contract domains.ProviderContract
		peer     domains.Peer
		bytes    int
		chars    int
		mentions bool
	}{
		{bale.Contract{}, domains.Peer{Type: "user", ID: "7"}, 65536, 0, true},
		{eitaameow.Contract{}, domains.Peer{Type: "user", ID: "7"}, 0, 4096, false},
		{rubikameow.Contract{}, domains.Peer{Type: "user", ID: "u0synthetic"}, 0, 4200, false},
	} {
		t.Run(string(tc.contract.Descriptor().ID), func(t *testing.T) {
			d := tc.contract.Descriptor().Send
			require.Equal(t, []string{"text", "file", "image", "audio", "voice", "video"}, d.Kinds)
			require.Equal(t, tc.bytes, d.MaxTextBytes)
			require.Equal(t, tc.chars, d.MaxTextCharacters)
			require.Equal(t, tc.mentions, d.MentionsSupported)
			require.True(t, d.ReplySupported)
			require.Contains(t, d.MediaFormatNotes.Voice, "Ogg Opus")
			if tc.chars > 0 {
				if tc.contract.Descriptor().ID == domains.ProviderRubika {
					require.Contains(t, d.MediaFormatNotes.Audio, "MP3")
				} else {
					require.Contains(t, d.MediaFormatNotes.Audio, "Ogg Opus")
				}
				require.Contains(t, d.MediaFormatNotes.Video, "MP4")
			}
			limit := tc.chars
			if tc.bytes > 0 {
				limit = tc.bytes / len("ب")
			}
			for _, kind := range d.Kinds {
				r := domains.SendRequest{Peer: tc.peer, Kind: kind, Text: strings.Repeat("ب", limit), ReplyMessageID: "1"}
				if kind != "text" {
					r.MediaID = "synthetic-media"
				}
				require.NoError(t, tc.contract.ValidateSend(r), "advertised boundary for %s", kind)
				r.Text += "ب"
				require.Error(t, tc.contract.ValidateSend(r), "oversized %s text/caption", kind)
			}
			r := domains.SendRequest{Peer: tc.peer, Kind: "text", Text: "synthetic", Mentions: []string{"1"}}
			if tc.mentions {
				require.Equal(t, 100, d.MaxMentions)
				for i := 2; i <= d.MaxMentions; i++ {
					r.Mentions = append(r.Mentions, strconv.Itoa(i))
				}
				require.NoError(t, tc.contract.ValidateSend(r))
				r.Mentions = append(r.Mentions, "101")
				require.Error(t, tc.contract.ValidateSend(r))
			} else {
				require.Zero(t, d.MaxMentions)
				require.Error(t, tc.contract.ValidateSend(r))
			}
			data, err := json.Marshal(tc.contract.Descriptor())
			require.NoError(t, err)
			var result map[string]any
			require.NoError(t, json.Unmarshal(data, &result))
			send := result["send"].(map[string]any)
			_, hasBytes := send["max_text_bytes"]
			_, hasChars := send["max_text_characters"]
			require.Equal(t, tc.bytes > 0, hasBytes)
			require.Equal(t, tc.chars > 0, hasChars)
		})
	}
}

func TestUnregisteredProviderDoesNotAdvertiseSends(t *testing.T) {
	r, err := domains.NewProviderRegistry()
	require.NoError(t, err)
	for _, descriptor := range r.List() {
		require.False(t, descriptor.Enabled)
		require.Empty(t, descriptor.Send.Kinds)
		require.NotNil(t, descriptor.Send.Kinds)
		require.False(t, descriptor.Send.ReplySupported)
		require.False(t, descriptor.Send.MentionsSupported)
	}
}
