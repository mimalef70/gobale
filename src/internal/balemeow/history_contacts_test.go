package balemeow

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestHistoryDialogsContactsOfficialShapes(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, msg *wire.ClientMessage) {
		if msg.Request == nil {
			return
		}
		r := msg.Request
		var response proto.Message
		switch r.Method {
		case "LoadHistory":
			q := &wire.HistoryRequest{}
			if decode(r.Payload, q) != nil || q.Peer.Id != 42 || q.Limit != 1 || q.LoadMode != 2 || q.Date <= 1720000000000 {
				t.Error("wrong history args")
			}
			response = &wire.HistoryResponse{History: []*wire.HistoryItem{{SenderId: 12345, Rid: 9007199254740993, Date: 1720000000000, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}, State: 2}}}
		case "LoadDialogs":
			q := &wire.DialogsRequest{}
			if decode(r.Payload, q) != nil || q.Limit != 1 || q.MinDate != -1 {
				t.Error("wrong dialog args")
			}
			response = &wire.DialogsResponse{Dialogs: []*wire.Dialog{{Peer: &wire.Peer{Type: 1, Id: 42}, SortDate: 1720000000000, Date: 1720000000000, Rid: 9007199254740993}}}
		case "GetContacts":
			response = &wire.ContactsResponse{Users: []*wire.User{{Id: 42, AccessHash: 91, Name: "Synthetic contact", Nick: &wire.StringValue{Value: "synthetic"}}}}
		case "SearchContacts":
			q := &wire.ContactsSearchRequest{}
			if decode(r.Payload, q) != nil || q.Request != "synthetic" {
				t.Error("wrong search args")
			}
			response = &wire.ContactsSearchResponse{UserPeers: []*wire.PeerRef{{Id: 43, AccessHash: 92}}, Groups: []*wire.Group{{Id: 77, Title: "Synthetic group", AccessHash: 93}}}
		default:
			t.Errorf("unexpected RPC %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	for _, tc := range []struct{ op, body, want string }{
		{"chat.history", `{"peer":{"type":"user","id":"42"},"limit":1}`, `"message_id":"9007199254740993"`},
		{"chat.list", `{"limit":1}`, `"next_date":"1720000000000"`},
		{"contacts.list", `{}`, `"name":"Synthetic contact"`},
		{"contacts.search", `{"query":"synthetic"}`, `"title":"Synthetic group"`},
	} {
		data, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
		if err != nil || !strings.Contains(string(data), tc.want) || strings.Contains(string(data), "access_hash") {
			t.Fatalf("%s: %s %v", tc.op, data, err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peerHashes["user:42"] != 91 || c.peerHashes["user:43"] != 92 || c.peerHashes["group:77"] != 93 {
		t.Fatal("private refs not cached")
	}
}

func TestHistoryRejectsOverfullPagesAndMalformedDocuments(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "negative_size", true: "too_many_items"}[overflow], func(t *testing.T) {
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				rows := []*wire.HistoryItem{{SenderId: 42, Rid: 1, Date: 1720000000000, Message: &wire.Message{Document: &wire.DocumentMessage{FileId: -123, FileSize: -1}}}}
				if overflow {
					rows[0].Message = nil
					rows = append(rows, rows[0])
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.HistoryResponse{History: rows})}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			_, err := c.Call(context.Background(), "chat.history", json.RawMessage(`{"peer":{"type":"user","id":"42"},"limit":1}`))
			if codeOf(err) != "PROTOCOL_ERROR" {
				t.Fatal(err)
			}
		})
	}
}
