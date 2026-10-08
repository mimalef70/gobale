package balemeow

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

func TestExtendedMessagePinsKeepExactIDsAndExPeerSemantics(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		if r.Method != "PinMessage" {
			t.Errorf("unexpected RPC %s", r.Method)
			return
		}
		q := &wire.ChatPinRequest{}
		if decode(r.Payload, q) != nil || q.ExPeer.Type != 3 || q.ExPeer.AccessHash != 9988 || q.Message.Rid != -9223372036854775808 || q.Message.Date != 1720000000000 || !q.JustMine {
			t.Errorf("incorrect pin encoding: %v", q)
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.Empty{})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("channel", &wire.PeerRef{Id: 77, AccessHash: 9988})
	out, err := c.Call(context.Background(), "message.pin", json.RawMessage(`{"peer":{"type":"channel","id":"77"},"message_id":"-9223372036854775808","date":"1720000000000","just_mine":true,"request_id":"9007199254740993"}`))
	if err != nil || string(out) != `{"acknowledged":true}` {
		t.Fatal(string(out), err)
	}
}

func TestExtendedFolderResultNeverLeaksPeerHash(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := &wire.FolderListResponse{Folders: []*wire.ChatFolder{{Id: 12, Name: "تیم 💙", Peers: []*wire.Peer{{Type: 3, Id: 77, AccessHash: 9999}, {Type: 4, Id: 42, AccessHash: 8888}}}}}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, r)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	out, err := c.Call(context.Background(), "folders.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Folders []struct {
			ID    string         `json:"id"`
			Peers []domains.Peer `json:"peers"`
		} `json:"folders"`
	}
	if json.Unmarshal(out, &body) != nil || len(body.Folders) != 1 || body.Folders[0].ID != "12" || body.Folders[0].Peers[0].Type != "channel" || body.Folders[0].Peers[1].Type != "user" {
		t.Fatal(string(out))
	}
	var raw map[string]any
	_ = json.Unmarshal(out, &raw)
	for _, p := range body.Folders[0].Peers {
		if p.AccessHash != "" {
			t.Fatal("private hash leaked")
		}
	}
}

func TestExtendedFolderMutationLostAckNeverResends(t *testing.T) {
	var writes atomic.Int32
	fake := newFakeWS(t, func(_ *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			writes.Add(1)
		}
	})
	c := fake.client()
	c.opts.RequestTimeout = 20 * time.Millisecond
	connectTest(t, c, acceptingSink)
	_, err := c.Call(context.Background(), "folders.create", json.RawMessage(`{"name":"Test","peers":[],"request_id":"44"}`))
	if codeOf(err) != "SEND_UNKNOWN" || writes.Load() != 1 {
		t.Fatal(err, writes.Load())
	}
}

func TestExtendedPinnedMessagesValidateHistoryContent(t *testing.T) {
	valid := func() *wire.HistoryItem {
		return &wire.HistoryItem{Rid: 77, SenderId: 42, Date: 1720000000000, Message: &wire.Message{Text: &wire.TextMessage{Text: "hello"}}}
	}
	cases := map[string]func(*wire.HistoryItem){
		"missing sender": func(m *wire.HistoryItem) { m.SenderId = 0 },
		"missing date":   func(m *wire.HistoryItem) { m.Date = 0 },
		"ambiguous union": func(m *wire.HistoryItem) {
			m.Message.Poll = &wire.PollMessage{Question: "conflicting content"}
		},
		"invalid quote": func(m *wire.HistoryItem) {
			m.QuotedMessage = &wire.QuotedMessage{MessageId: &wire.Int64Value{Value: 0}}
		},
		"excessive nesting": func(m *wire.HistoryItem) {
			for range maxContentDepth {
				m.Message = &wire.Message{Template: &wire.TemplateMessage{Message: m.Message}}
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			item := valid()
			change(item)
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request != nil {
					fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.ChatPinsResponse{Messages: []*wire.HistoryItem{item}})}})
				}
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 9999})
			_, err := c.Call(context.Background(), "message.pins", json.RawMessage(`{"peer":{"type":"user","id":"42"}}`))
			if codeOf(err) != "PROTOCOL_ERROR" {
				t.Fatalf("malformed pinned message accepted: %v", err)
			}
		})
	}
}

func TestExtendedPinnedMessagesPreserveForwardMetadata(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		item := &wire.HistoryItem{Rid: 77, SenderId: 42, Date: 1720000000000,
			Message: &wire.Message{Empty: &wire.Empty{}},
			QuotedMessage: &wire.QuotedMessage{SenderUserId: 43, Date: 1710000000000,
				MessageId: &wire.Int64Value{Value: -9007199254740993},
				Message:   &wire.Message{Text: &wire.TextMessage{Text: "original"}}}}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.ChatPinsResponse{Messages: []*wire.HistoryItem{item}})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 9999})
	out, err := c.Call(context.Background(), "message.pins", json.RawMessage(`{"peer":{"type":"user","id":"42"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []struct {
			Payload struct {
				Kind  string `json:"kind"`
				Quote struct {
					MessageID string `json:"message_id"`
				} `json:"forwarded_from"`
			} `json:"payload"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &body); err != nil || len(body.Messages) != 1 || body.Messages[0].Payload.Kind != "text" || body.Messages[0].Payload.Quote.MessageID != "-9007199254740993" {
		t.Fatalf("forward metadata lost: %s (%v)", out, err)
	}
}
