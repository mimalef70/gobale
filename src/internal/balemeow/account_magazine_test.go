package balemeow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func TestMagazineUsesOpaqueCursorAndMessageDate(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var out proto.Message
		if r.Method == "GetMessageUpvoters" {
			q := &wire.AccountUpvotersRequest{}
			if decode(r.Payload, q) != nil || q.Message.Rid != -9007199254740993 || q.Message.Date != 1720000000000 || q.Message.Peer.AccessHash != 99 || string(q.Next.Value) != string([]byte{0, 255, 1}) {
				t.Error("cursor/message wire mismatch")
			}
			out = &wire.AccountUpvotersResponse{Next: &wire.BytesValue{Value: []byte{255, 0, 1}}, Users: []*wire.PeerRef{{Id: 42, AccessHash: 999}}}
		} else {
			q := &wire.AccountUpvoteRequest{}
			if decode(r.Payload, q) != nil || q.Message.Rid != -9007199254740993 || q.AlbumId.Value != -2 {
				t.Error("invalid upvote target")
			}
			out = &wire.AccountUpvoteResponse{Upvotes: &wire.AccountUpvotes{Messages: []*wire.AccountMagazineMessage{q.Message}, Limit: 10}}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, out)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 99})
	base := `"peer":{"type":"user","id":"42"},"message_id":"-9007199254740993","date_ms":"1720000000000"`
	for _, op := range []string{"message.upvote", "message.upvote.remove", "message.upvoters"} {
		extra := `,"request_id":"1","album_id":"-2"`
		want := `"upvotes_limit":10`
		if op == "message.upvoters" {
			extra = `,"next":"AP8B"`
			want = `"next":"_wAB"`
		}
		raw, err := c.accountBusinessCall(context.Background(), op, json.RawMessage(`{`+base+extra+`}`))
		if err != nil || !strings.Contains(string(raw), want) || strings.Contains(string(raw), "access_hash") {
			t.Fatalf("%s %v", raw, err)
		}
	}
}
