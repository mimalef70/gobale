package rest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderDiscoveryExposesOrdinarySendLimits(t *testing.T) {
	s, _ := setupAPI(t, "")
	for _, path := range []string{"/app/providers", "/app/info"} {
		status, body := apiRequest(t, s, "GET", path, "", nil)
		require.Equal(t, 200, status)
		result := body["results"]
		if path == "/app/info" {
			result = result.(map[string]any)["providers"]
		}
		providers := result.([]any)
		require.Len(t, providers, 3)
		bale := providers[0].(map[string]any)
		require.Equal(t, "bale", bale["id"])
		send := bale["send"].(map[string]any)
		require.Equal(t, float64(65536), send["max_text_bytes"])
		require.NotContains(t, send, "max_text_characters")
		require.Equal(t, true, send["mentions_supported"])
		require.Equal(t, float64(100), send["max_mentions"])
		require.Equal(t, true, send["reply_supported"])
		require.Len(t, send["kinds"], 6)
		require.Contains(t, send["media_format_notes"].(map[string]any)["voice"], "Ogg Opus")
		interactions := bale["interactions"].(map[string]any)
		require.Equal(t, "timestamp_watermark", interactions["receipt_model"])
		require.Equal(t, "date", interactions["read_argument"])
		require.Equal(t, "presence.typing", interactions["typing"].(map[string]any)["operation"])
		require.Equal(t, true, interactions["delivered_events"])
	}
}
