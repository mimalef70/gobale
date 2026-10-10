package domains

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// A consumer can apply every provider's reviewed edit using the same target and
// presence rules, without interpreting native metadata as replacement content.
func TestMessageEnvelopeAndPatchRoundTrip(t *testing.T) {
	for _, provider := range []Provider{ProviderBale, ProviderEitaa, ProviderRubika} {
		t.Run(string(provider), func(t *testing.T) {
			event := Event{Provider: provider, ID: "event", Type: "message.edited", Peer: Peer{Type: "user", ID: "opaque-peer"}, MessageID: "42", Direction: "unknown", Time: time.Unix(100, 0).UTC(), Payload: json.RawMessage(`{"reviewed":true}`), Message: &Message{ID: "42", ChatID: "opaque-peer", OriginalMessageID: "42", Body: "سلام 🙂", Supported: true}}
			raw, err := json.Marshal(event)
			require.NoError(t, err)
			var public map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &public))
			require.NotContains(t, public, "message")
			require.NotContains(t, public, "message_patch")
			var message Message
			require.NoError(t, json.Unmarshal(public["payload"], &message))
			require.Equal(t, event.Message, messagePointer(message))
			require.Contains(t, string(public["payload"]), `"partial":false`)
			var restored Event
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.Equal(t, event, restored)
		})
	}
	for _, tc := range []struct {
		name string
		body *string
	}{{"absent", nil}, {"empty", new("")}, {"unicode", new("سلام %_🙂")}} {
		t.Run(tc.name, func(t *testing.T) {
			event := Event{Provider: ProviderRubika, Type: "message.edited", Peer: Peer{Type: "user", ID: "u0peer"}, MessageID: "42", Direction: "unknown", Payload: json.RawMessage(`{"partial":true,"provider_update_timestamp":"opaque"}`), MessagePatch: &MessagePatch{ID: "42", ChatID: "u0peer", OriginalMessageID: "42", Partial: true, Body: tc.body, Supported: tc.body != nil}}
			raw, err := json.Marshal(event)
			require.NoError(t, err)
			var public map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &public))
			require.NotContains(t, public, "message")
			require.NotContains(t, public, "message_patch")
			var payload map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(public["payload"], &payload))
			_, hasBody := payload["body"]
			require.Equal(t, tc.body != nil, hasBody)
			var restored Event
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.Nil(t, restored.Message)
			require.Equal(t, event, restored)
			text := "keep original"
			if restored.MessagePatch.Body != nil {
				text = *restored.MessagePatch.Body
			}
			if tc.body == nil {
				require.Equal(t, "keep original", text)
			} else {
				require.Equal(t, *tc.body, text)
			}
		})
	}
}
func messagePointer(m Message) *Message { return &m }

func TestHistoricalMessageEnvelopeDecodesWithoutRewritingIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"event_id":"old","event":"message.edited","peer":{"type":"user","id":"7"},"message_id":"42","payload":{"partial":true,"text":"old patch","timestamp_source":"observed"}}`,
		`{"event_id":"old","event":"message","peer":{"type":"user","id":"7"},"message_id":"42","content":{"text":"old message"},"payload":{"id":"42","chat_id":"user:7","body":"old message","supported":true}}`,
	} {
		var event Event
		require.NoError(t, json.Unmarshal([]byte(raw), &event))
		require.Equal(t, "old", event.ID)
		require.Nil(t, event.MessagePatch) // old native patches are not silently upgraded
		if event.Message != nil {
			require.Equal(t, "user:7", event.Message.ChatID)
		}
	}
}
