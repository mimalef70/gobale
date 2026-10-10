package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func TestPresenceAndReactionsReviewedWireAndStringIDs(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var out proto.Message = &wire.Empty{}
		switch r.Method {
		case "Typing":
			if !bytes.Equal(r.Payload, []byte{10, 6, 8, 1, 16, 42, 24, 99, 24, 2}) {
				t.Errorf("Typing must use field3: %x", r.Payload)
			}
		case "StopTyping":
			if !bytes.Equal(r.Payload, []byte{10, 6, 8, 1, 16, 42, 24, 99, 16, 2}) {
				t.Errorf("StopTyping must use field2: %x", r.Payload)
			}
		case "SetOnline":
			v := &wire.PresenceOnlineRequest{}
			if decode(r.Payload, v) != nil || v.IsOnline || v.Timeout != 90000 || v.DeviceType != 2 {
				t.Error("explicit offline lost")
			}
		case "GetUsersPresence":
			v := &wire.PresenceUsersRequest{}
			if decode(r.Payload, v) != nil || len(v.UserIds) != 1 || v.UserIds[0] != 42 {
				t.Error("wrong user IDs")
			}
			out = &wire.PresenceResponse{Presences: []*wire.PresenceRecord{{Online: &wire.PresenceOnline{Peer: &wire.PeerRef{Id: 42, AccessHash: 99}, ExpiresAt: &wire.Int64Value{Value: 1720000000123}}}}}
		case "GetContactsPresences", "GetGroupMembersPresences":
			out = &wire.PresenceResponse{Presences: []*wire.PresenceRecord{{Offline: &wire.PresenceOffline{Peer: &wire.PeerRef{Id: 42, AccessHash: 99}, LastSeenUnknown: &wire.BoolValue{Value: true}, UnknownLastSeenValue: 1}}}}
		case "GetGroupOnlineCount":
			out = &wire.PresenceCountResponse{Count: 3}
		case "MessageSetReaction", "MessageRemoveReaction":
			v := &wire.ReactionMutationRequest{}
			if decode(r.Payload, v) != nil || v.Peer.AccessHash != 99 || v.Rid != -9007199254740993 || v.Date != 1720000000123 || v.Code != "💙" {
				t.Error("reaction ID/date/ref incorrect")
			}
			out = &wire.ReactionMutationResponse{Reactions: []*wire.ReactionSummary{{Users: []uint32{42}, Code: "💙", Count: &wire.Int64Value{Value: 1}}}}
		case "GetMessageReactionsList":
			v := &wire.ReactionUsersRequest{}
			if decode(r.Payload, v) != nil || v.Rid != -9007199254740993 || v.Date != 1720000000123 || v.Limit != 20 {
				t.Error("reaction pagination args")
			}
			out = &wire.ReactionUsersResponse{Users: []*wire.ReactionUser{{UserId: 42, Code: "💙", ReactionTime: 1720000000123}}}
		case "GetMessagesReactions":
			v := &wire.ReactionsRequest{}
			if decode(r.Payload, v) != nil || len(v.Mids) != 1 || v.Mids[0].Rid != -9007199254740993 {
				t.Error("must encode nested mids, not packed RIDs")
			}
			out = &wire.ReactionsResponse{Containers: []*wire.ReactionContainer{{Rid: -9007199254740993, Date: 1720000000123, Reactions: []*wire.ReactionSummary{{Code: "💙", Count: &wire.Int64Value{Value: 1}}}}}}
		case "GetMessagesViews":
			v := &wire.ViewsRequest{}
			if decode(r.Payload, v) != nil || len(v.Mids) != 1 || v.Mids[0].Rid != -9007199254740993 {
				t.Error("wrong view mids")
			}
			out = &wire.ViewsResponse{Containers: []*wire.ViewContainer{{Mid: v.Mids[0], Views: &wire.Int64Value{Value: 9007199254740993}}}}
		default:
			t.Errorf("unexpected presence operation %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, out)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 99})
	c.rememberRef("group", &wire.PeerRef{Id: 77, AccessHash: 88})
	peer := `"peer":{"type":"user","id":"42"}`
	mid := `"message_id":"-9007199254740993","date_ms":"1720000000123"`
	messages := `"messages":[{` + mid + `}]`
	for _, tc := range []struct{ op, body, want string }{
		{"presence.online", `"online":false`, `"ephemeral":true`}, {"presence.typing", peer + `,"typing_type":2`, `"ephemeral":true`}, {"presence.stop", peer + `,"typing_type":2`, `"ephemeral":true`},
		{"presence.users", `"users":[{"type":"user","id":"42"}]`, `"expires_at_ms":"1720000000123"`}, {"presence.contacts", `"limit":10`, `"last_seen_unknown":true`}, {"presence.group", `"peer":{"type":"group","id":"77"}`, `"id":"42"`}, {"presence.group.count", `"peer":{"type":"group","id":"77"}`, `"count":3`},
		{"message.reaction.set", peer + `,` + mid + `,"code":"💙","request_id":"1"`, `"count":"1"`}, {"message.reaction.remove", peer + `,` + mid + `,"code":"💙","request_id":"2"`, `"acknowledged":true`},
		{"message.reaction.users", peer + `,` + mid, `"user_id":"42"`}, {"message.reactions", peer + `,` + messages, `"message_id":"-9007199254740993"`}, {"message.views", peer + `,` + messages, `"incremented":false`}, {"message.views.increment", peer + `,` + messages + `,"request_id":"3"`, `"views":"9007199254740993"`},
	} {
		v, e := c.presenceReactionCall(context.Background(), tc.op, json.RawMessage(`{`+tc.body+`}`))
		if e != nil || !strings.Contains(string(v), tc.want) || strings.Contains(string(v), "access_hash") {
			t.Fatalf("%s: %s %v", tc.op, v, e)
		}
	}
}

func TestPresenceReactionValidationAndNoBlindRetry(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body string }{
		{"presence.online", `{}`}, {"presence.online", `{"online":true,"timeout_ms":90001}`}, {"presence.typing", `{"typing_type":13}`},
		{"presence.users", `{"users":[]}`}, {"message.views", `{"messages":[{"message_id":"1","date_ms":1}]}`}, {"message.views", `{"messages":[{"message_id":"0","date_ms":"1"}]}`},
		{"message.reaction.set", `{"request_id":"1","message_id":"1","date_ms":"1","code":""}`}, {"message.reactions", `{"messages":[{"message_id":"1","date_ms":"1"},{"message_id":"1","date_ms":"2"}]}`},
	} {
		_, e := c.presenceReactionCall(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(e) != "INVALID_REQUEST" {
			t.Fatalf("%s: %v", tc.op, e)
		}
	}
	var writes atomic.Int32
	fake := newFakeWS(t, func(_ *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			writes.Add(1)
		}
	})
	c = fake.client()
	c.opts.RequestTimeout = 25 * time.Millisecond
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 99})
	_, e := c.presenceReactionCall(context.Background(), "message.reaction.set", json.RawMessage(`{"peer":{"type":"user","id":"42"},"message_id":"-1","date_ms":"1","code":"x","request_id":"1"}`))
	if codeOf(e) != "SEND_UNKNOWN" || writes.Load() != 1 {
		t.Fatalf("%v calls%d", e, writes.Load())
	}
}

func TestPresenceAndReactionsRejectForeignProviderResults(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		var p proto.Message
		if m.Request.Method == "GetUsersPresence" {
			p = &wire.PresenceResponse{Presences: []*wire.PresenceRecord{{Online: &wire.PresenceOnline{Peer: &wire.PeerRef{Id: 666}}}}}
		} else {
			p = &wire.ViewsResponse{Containers: []*wire.ViewContainer{{Mid: &wire.ReactionMessageID{Rid: 666}, Views: &wire.Int64Value{Value: 1}}}}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, p)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 99})
	for _, tc := range []struct{ op, body string }{{"presence.users", `{"users":[{"type":"user","id":"42"}]}`}, {"message.views", `{"peer":{"type":"user","id":"42"},"messages":[{"message_id":"1","date_ms":"1"}]}`}} {
		_, e := c.presenceReactionCall(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(e) != "PROTOCOL_ERROR" {
			t.Fatal(e)
		}
	}
}
