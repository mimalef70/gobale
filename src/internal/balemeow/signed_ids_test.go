package balemeow

import (
	"encoding/json"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"testing"
)

func TestProviderMessageIDsAcceptSignedInt64(t *testing.T) {
	for _, id := range []string{"-1", "-9223372036854775808", "9223372036854775807"} {
		if _, err := messageID(id); err != nil {
			t.Fatalf("valid signed ID %q: %v", id, err)
		}
	}
	for _, id := range []string{"0", "-0", "+1", "--1", " 1", "9223372036854775808"} {
		if _, err := messageID(id); err == nil {
			t.Fatalf("invalid signed ID %q accepted", id)
		}
	}
	peer := &wire.Peer{Type: 1, Id: 42}
	for _, update := range []*wire.UpdateContainer{
		{Message: &wire.UpdateMessage{Peer: peer, SenderId: 42, Rid: -9223372036854775808, Date: 1720000000000, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}}},
		{Edited: &wire.UpdateMessageEdited{Peer: peer, Rid: -9223372036854775808, Date: &wire.Int64Value{Value: 1720000000000}, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}}},
		{Deleted: &wire.UpdateMessageDeleted{Peer: peer, Rids: []int64{-9223372036854775808}}},
		{Sent: &wire.UpdateMessageSent{Peer: peer, Rid: -9223372036854775808, Date: 1720000000000}},
	} {
		events, err := decodeEvents("12345", marshal(t, update))
		if err != nil || len(events) != 1 || events[0].MessageID != "-9223372036854775808" {
			b, _ := json.Marshal(events)
			t.Fatalf("signed ID decode: %s %v", b, err)
		}
	}
}
