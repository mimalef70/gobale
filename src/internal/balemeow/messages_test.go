package balemeow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func TestReviewedForwardAndDeletePreserveIDsAndExplicitScope(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, envelope *wire.ClientMessage) {
		r := envelope.Request
		if r == nil {
			return
		}
		if r.Service != "bale.messaging.v2.Messaging" {
			t.Error("wrong service", r.Service)
		}
		var response proto.Message
		switch r.Method {
		case "ForwardMessages":
			q := &wire.ForwardMessagesRequest{}
			if decode(r.Payload, q) != nil || q.Peer.Id != 43 || len(q.Rids) != 1 || q.Rids[0] != 9007199254740993 || len(q.ForwardedMessages) != 1 || q.ForwardedMessages[0].Rid != 9007199254740995 || q.ForwardedMessages[0].Peer.Id != 42 || q.ForwardedMessages[0].GetDate().GetValue() != 1720000000000 || !q.HideSender {
				t.Errorf("invalid forward: %+v", q)
			}
			response = &wire.SendMessageResponse{Sequence: 21, Date: 1720000000100}
		case "DeleteMessage":
			q := &wire.DeleteMessageRequest{}
			if decode(r.Payload, q) != nil || q.Peer.Id != 42 || len(q.Rids) != 1 || q.Rids[0] != 9007199254740995 || q.JustMine == nil || q.JustMine.Value || q.Dates == nil || len(q.Dates.Dates) != 1 || q.Dates.Dates[0] != 1720000000000 {
				t.Errorf("invalid explicit delete: %+v", q)
			}
			response = &wire.Empty{}
		default:
			t.Error("unexpected method", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	tests := []struct{ operation, body, want string }{
		{"message.forward", `{"peer":{"type":"user","id":"43"},"source_peer":{"type":"user","id":"42"},"message_id":"9007199254740995","source_date":"1720000000000","hide_sender":true,"request_id":"9007199254740993"}`, `"message_id":"9007199254740993"`},
		{"message.delete", `{"peer":{"type":"user","id":"42"},"message_id":"9007199254740995","date":"1720000000000","just_mine":false,"request_id":"9007199254740994"}`, `"just_mine":false`},
	}
	for _, test := range tests {
		result, err := c.Call(context.Background(), test.operation, json.RawMessage(test.body))
		if err != nil || !strings.Contains(string(result), test.want) {
			t.Fatal(test.operation, string(result), err)
		}
	}
}
func TestMessageMutationRequiresJournalIDAndDeletionScope(t *testing.T) {
	c := New(Options{})
	tests := []struct{ operation, body, code string }{
		{"message.forward", `{"peer":{"type":"user","id":"43"},"source_peer":{"type":"user","id":"42"},"message_id":"7"}`, "INVALID_REQUEST_ID"},
		{"message.forward", `{"peer":{"type":"user","id":"43"},"source_peer":{"type":"user","id":"42"},"message_id":"7","request_id":"9"}`, "INVALID_REQUEST"},
		{"message.delete", `{"peer":{"type":"user","id":"42"},"message_id":"7","source_date":"1720000000000","request_id":"9"}`, "INVALID_REQUEST"},
	}
	for _, test := range tests {
		_, err := c.Call(context.Background(), test.operation, json.RawMessage(test.body))
		if codeOf(err) != test.code {
			t.Fatal(test, err)
		}
	}
}
func TestMalformedForwardAcknowledgementIsAmbiguous(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, envelope *wire.ClientMessage) {
		if r := envelope.Request; r != nil {
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: []byte{255}}})
		}
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	_, err := c.Call(context.Background(), "message.forward", json.RawMessage(`{"peer":{"type":"user","id":"43"},"source_peer":{"type":"user","id":"42"},"message_id":"7","source_date":"1720000000000","request_id":"9"}`))
	if codeOf(err) != "SEND_UNKNOWN" {
		t.Fatal(err)
	}
}

func TestForwardResolvesBothGroupAccessReferencesInternally(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, envelope *wire.ClientMessage) {
		r := envelope.Request
		if r == nil {
			return
		}
		var response proto.Message
		switch r.Method {
		case "GetMyGroups":
			response = &wire.GetMyGroupsResponse{Groups: []*wire.PeerRef{{Id: 77, AccessHash: 700}, {Id: 78, AccessHash: 800}}}
		case "ForwardMessages":
			q := &wire.ForwardMessagesRequest{}
			if decode(r.Payload, q) != nil || q.Peer.Id != 77 || q.Peer.AccessHash != 700 || len(q.ForwardedMessages) != 1 || q.ForwardedMessages[0].Peer.Id != 78 || q.ForwardedMessages[0].Peer.AccessHash != 800 {
				t.Errorf("missing internally resolved group references: %+v", q)
			}
			response = &wire.SendMessageResponse{Sequence: 1, Date: 1720000000100}
		default:
			t.Errorf("unexpected method %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	result, err := c.Call(context.Background(), "message.forward", json.RawMessage(`{"peer":{"type":"group","id":"77"},"source_peer":{"type":"group","id":"78"},"message_id":"7","source_date":"1720000000000","request_id":"9"}`))
	if err != nil || strings.Contains(string(result), "access_hash") {
		t.Fatal(string(result), err)
	}
}
