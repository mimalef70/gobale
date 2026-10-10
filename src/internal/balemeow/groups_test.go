package balemeow

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestReviewedGroupCalls(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, msg *wire.ClientMessage) {
		if msg.Request == nil {
			return
		}
		r := msg.Request
		if r.Service != groupsService {
			t.Error("wrong service")
		}
		var response proto.Message
		switch r.Method {
		case "GetMyGroups":
			q := &wire.GetMyGroupsRequest{}
			if decode(r.Payload, q) != nil || q.Mode != 1 {
				t.Error("wrong group mode")
			}
			response = &wire.GetMyGroupsResponse{Groups: []*wire.PeerRef{{Id: 77, AccessHash: 9223372036854775806}}}
		case "LoadMembers":
			q := &wire.GroupMembersRequest{}
			if decode(r.Payload, q) != nil || q.Group.AccessHash != 9223372036854775806 || q.Limit != 20 {
				t.Error("wrong member request")
			}
			response = &wire.GroupMembersResponse{Members: []*wire.GroupMember{{Uid: 42, InviterUid: 7, Date: 1720000000000, IsAdmin: &wire.BoolValue{Value: true}}}, Next: &wire.BytesValue{Value: []byte{0, 1, 255}}}
		case "CreateGroup":
			q := &wire.GroupCreateRequest{}
			if decode(r.Payload, q) != nil || q.Rid != 9007199254740993 || q.Title != "Synthetic group" {
				t.Error("wrong create request")
			}
			response = &wire.GroupCreateResponse{Group: &wire.Group{Id: 78, AccessHash: 92, Title: q.Title}}
		case "InviteUsers":
			q := &wire.GroupInviteRequest{}
			if decode(r.Payload, q) != nil || q.Group.Id != 78 || len(q.Users) != 1 || q.Users[0].Id != 42 {
				t.Error("wrong invite request")
			}
			response = &wire.GroupInviteResponse{NotAdded: []*wire.PeerRef{{Id: 42}}}
		case "EditGroupTitle":
			q := &wire.GroupTitleRequest{}
			if decode(r.Payload, q) != nil || q.Rid != 9007199254740995 || q.Title != "New title" {
				t.Error("wrong title request")
			}
			response = &wire.SendMessageResponse{Sequence: 5, Date: 1720000000000}
		default:
			t.Errorf("unexpected call %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 7})
	for _, test := range []struct{ op, body, want string }{
		{"group.list", `{}`, `"id":"77"`},
		{"group.members", `{"peer":{"type":"group","id":"77"}}`, `"next":"AAH_"`},
		{"group.create", `{"title":"Synthetic group","request_id":"9007199254740993"}`, `"id":"78"`},
		{"group.invite", `{"peer":{"type":"group","id":"78"},"users":[{"type":"user","id":"42"}],"request_id":"9007199254740994"}`, `"not_added_user_ids":["42"]`},
		{"group.title", `{"peer":{"type":"group","id":"78"},"title":"New title","request_id":"9007199254740995"}`, `"acknowledged":true`},
	} {
		data, err := c.Call(context.Background(), test.op, json.RawMessage(test.body))
		if err != nil || !strings.Contains(string(data), test.want) {
			t.Fatalf("%s: %s %v", test.op, data, err)
		}
		if strings.Contains(string(data), "access_hash") {
			t.Fatal("provider access hash exposed")
		}
	}
}
func TestGroupValidationBeforeNetwork(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body, code string }{
		{"group.create", `{"title":"x"}`, "INVALID_REQUEST_ID"},
		{"group.create", `{"request_id":"1","title":""}`, "INVALID_REQUEST"},
		{"group.members", `{"peer":{"type":"group","id":"77"}}`, "CONNECTION_UNAVAILABLE"},
		{"group.invite", `{"request_id":"1","users":[{"type":"user","id":"42"}]}`, "CONNECTION_UNAVAILABLE"},
	} {
		_, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(err) != tc.code {
			t.Fatalf("%s got %v", tc.op, err)
		}
	}
}

func TestCurrentStreamWrapperAndEditWrappers(t *testing.T) {
	raw := marshal(t, &wire.UpdateContainer{Edited: &wire.UpdateMessageEdited{Peer: &wire.Peer{Type: 1, Id: 42}, Rid: 9007199254740993, Date: &wire.Int64Value{Value: 1720000000000}, UpdaterUserId: &wire.Int32Value{Value: 12345}, Message: &wire.Message{Text: &wire.TextMessage{Text: "edited"}}}})
	stream := marshal(t, &wire.StreamUpdate{Update: raw, Sequence: 10, Timestamp: 1720000000000})
	events, err := decodeStreamEvents("12345", stream)
	if err != nil || len(events) != 1 {
		t.Fatalf("%v %v", events, err)
	}
	if events[0].Type != "message.edited" || events[0].Direction != "outgoing" || events[0].MessageID != "9007199254740993" || events[0].Checkpoint != "" {
		t.Fatal(events[0])
	}
	sent := marshal(t, &wire.UpdateContainer{Sent: &wire.UpdateMessageSent{Peer: &wire.Peer{Type: 1, Id: 42}, Rid: 9007199254740993, Date: 1720000000000}})
	events, err = decodeStreamEvents("12345", marshal(t, &wire.StreamUpdate{Updates: &wire.UpdateBatch{Updates: [][]byte{sent, raw}}}))
	if err != nil || len(events) != 2 || events[0].Type != "message.accepted" {
		t.Fatalf("%v %v", events, err)
	}
}
