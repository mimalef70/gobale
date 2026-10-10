package domains

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestWebhookFilterExactSetsAndUnknownActors(t *testing.T) {
	f := WebhookFilter{Peers: []Peer{{Type: "group", ID: "42"}}, PeerTypes: []string{"group"}, SenderIDs: []string{"7"}, ExcludeSenderIDs: []string{"8"}, Directions: []string{"incoming"}}
	require.NoError(t, f.Validate())
	ev := Event{Type: "message", Peer: Peer{Type: "group", ID: "42"}, SenderID: "7", Direction: "incoming"}
	require.True(t, f.Matches(ev))
	ev.SenderID = "8"
	require.False(t, f.Matches(ev))
	ev.SenderID = "7"
	ev.Peer.Type = "user"
	require.False(t, f.Matches(ev))
	unknown := WebhookFilter{Directions: []string{"unknown"}}
	for _, sender := range []string{"", "0"} {
		require.True(t, unknown.Matches(Event{Type: "message.edited", SenderID: sender, Direction: "incoming"}))
	}
	require.True(t, unknown.Matches(Event{Type: "message.deleted", Peer: Peer{Type: "user", ID: "7"}}))
	require.False(t, WebhookFilter{SenderIDs: []string{"7"}}.Matches(Event{Peer: Peer{Type: "user", ID: "7"}}), "peer is not an actor")
	for _, raw := range []string{`null`, `{"sender_ids":[""]}`, `{"sender_ids":["1","1"]}`, `{"directions":["sideways"]}`, `{"peers":[{"type":"user","id":"1"},{"type":"user","id":"1"}]}`, `{"unsupported":true}`} {
		var got WebhookFilter
		require.Error(t, json.Unmarshal([]byte(raw), &got), raw)
	}
	var empty WebhookFilter
	require.NoError(t, json.Unmarshal([]byte(`{}`), &empty))
	require.True(t, empty.Matches(ev))
}
func TestQueryFilterValidation(t *testing.T) {
	require.Error(t, (EventFilter{Search: strings.Repeat("x", 513)}).Validate())
	require.Error(t, (EventFilter{Search: string([]byte{0xff})}).Validate())
	require.Error(t, (EventFilter{SenderID: "bad\nID"}).Validate())
	require.Error(t, (OperationFilter{State: "done"}).Validate())
	require.Error(t, (ScheduleFilter{Kind: "unknown"}).Validate())
	require.NoError(t, (EventFilter{Search: "سلام %_🙂\n", Peer: "group:42"}).Validate())
}

func TestOpaqueProviderDirection(t *testing.T) {
	require.Equal(t, "incoming", EventDirection(Event{Provider: ProviderRubika, Type: "message", SenderID: "u0synthetic", Direction: "incoming"}))
	require.Equal(t, "unknown", EventDirection(Event{Provider: ProviderBale, Type: "message", SenderID: "u0synthetic", Direction: "incoming"}))
	require.Equal(t, "unknown", EventDirection(Event{Provider: ProviderRubika, Type: "message", Direction: "incoming"}))
}
