package balemeow

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGroupDescriptionAndRemovalUsePrivateReferencesAndRID(t *testing.T) {
	var calls atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, msg *wire.ClientMessage) {
		if msg.Request == nil {
			return
		}
		r := msg.Request
		calls.Add(1)
		if r.Service != groupsService {
			t.Error("wrong service")
		}
		switch r.Method {
		case "EditGroupAbout":
			q := &wire.GroupDescriptionRequest{}
			if decode(r.Payload, q) != nil || q.Group.Id != 77 || q.Group.AccessHash != 88 || q.Rid != 9007199254740993 || q.About == nil || q.About.Value != "" {
				t.Error("empty description did not retain presence/private ref/RID")
			}
		case "KickUser":
			q := &wire.GroupRemoveRequest{}
			if decode(r.Payload, q) != nil || q.Group.Id != 77 || q.Group.AccessHash != 88 || q.Rid != 9007199254740994 || q.User.Id != 42 || q.User.AccessHash != 99 {
				t.Error("wrong removal wire fields")
			}
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.SendMessageResponse{Sequence: 3, Date: 1720000000000})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("group", &wire.PeerRef{Id: 77, AccessHash: 88})
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 99})
	for _, tc := range []struct{ op, body string }{
		{"group.description", `{"peer":{"type":"group","id":"77"},"description":"","request_id":"9007199254740993"}`},
		{"group.remove", `{"peer":{"type":"group","id":"77"},"user":{"type":"user","id":"42"},"request_id":"9007199254740994"}`},
	} {
		response, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
		if err != nil || string(response) != `{"acknowledged":true}` {
			t.Fatalf("%s: %s %v", tc.op, response, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected extra RPC")
	}
}

func TestGroupNewMutationValidationAndAmbiguity(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body, code string }{
		{"group.description", `{"peer":{"type":"group","id":"77"},"request_id":"1"}`, "INVALID_REQUEST"},
		{"group.description", `{"peer":{"type":"group","id":"77"},"request_id":"1","description":null}`, "INVALID_REQUEST"},
		{"group.description", `{"peer":{"type":"group","id":"77"},"request_id":"1","description":"` + strings.Repeat("x", 4097) + `"}`, "INVALID_REQUEST"},
		{"group.remove", `{"peer":{"type":"group","id":"77"},"request_id":"1","user":{"type":"group","id":"42"}}`, "INVALID_PEER"},
		{"group.remove", `{"peer":{"type":"group","id":"77"},"user":{"type":"user","id":"42"}}`, "INVALID_REQUEST_ID"},
	} {
		_, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(err) != tc.code {
			t.Fatalf("%s got %v", tc.op, err)
		}
	}
	var writes atomic.Int32
	fake := newFakeWS(t, func(_ *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			writes.Add(1)
		}
	})
	c = fake.client()
	c.opts.RequestTimeout = 20 * time.Millisecond
	connectTest(t, c, acceptingSink)
	c.rememberRef("group", &wire.PeerRef{Id: 77, AccessHash: 88})
	_, err := c.Call(context.Background(), "group.description", json.RawMessage(`{"peer":{"type":"group","id":"77"},"description":"test","request_id":"1"}`))
	if codeOf(err) != "SEND_UNKNOWN" || writes.Load() != 1 {
		t.Fatalf("uncertain write retried or falsely successful: %v writes=%d", err, writes.Load())
	}
}
