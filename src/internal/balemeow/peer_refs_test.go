package balemeow

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dialogPage(n int, date int64) []*wire.Dialog {
	rows := make([]*wire.Dialog, n)
	for i := range rows {
		rows[i] = &wire.Dialog{Peer: &wire.Peer{Type: 1, Id: uint32(i + 1)}, SortDate: date, Date: date}
	}
	return rows
}
func TestNonContactGroupUserResolvedFromPrivateDialogRefs(t *testing.T) {
	var pages, mutations atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var response proto.Message
		switch r.Method {
		case "GetContacts":
			response = &wire.ContactsResponse{}
		case "LoadDialogs":
			q := &wire.DialogsRequest{}
			if decode(r.Payload, q) != nil || q.Limit != 100 {
				t.Fatal("wrong dialog lookup")
			}
			if pages.Add(1) == 1 {
				if q.MinDate != -1 {
					t.Error("first cursor must be provider -1 sentinel")
				}
				response = &wire.DialogsResponse{Dialogs: dialogPage(100, 1000)}
			} else {
				if q.MinDate != 1000 {
					t.Error("wrong next cursor")
				}
				response = &wire.DialogsResponse{UserPeers: []*wire.PeerRef{{Id: 42, AccessHash: 7890123456789}}}
			}
		case "CreateGroup":
			mutations.Add(1)
			q := &wire.GroupCreateRequest{}
			if decode(r.Payload, q) != nil || len(q.Users) != 1 || q.Users[0].Id != 42 || q.Users[0].AccessHash != 7890123456789 {
				t.Error("non-contact private ref lost")
			}
			response = &wire.GroupCreateResponse{Group: &wire.Group{Id: 77, AccessHash: 88, Title: "Synthetic"}}
		default:
			t.Errorf("unexpected method %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	result, err := c.Call(context.Background(), "group.create", json.RawMessage(`{"title":"Synthetic","users":[{"type":"user","id":"42"}],"request_id":"1"}`))
	if err != nil || mutations.Load() != 1 || pages.Load() != 2 || strings.Contains(string(result), "7890123456789") {
		t.Fatalf("unsafe reference lookup: %s %v pages=%d writes=%d", result, err, pages.Load(), mutations.Load())
	}
	// A different client/account cannot obtain this private reference from c.
	isolated := fake.client()
	if _, ok := isolated.cachedUserRef(domains.Peer{Type: "user", ID: "42"}, 42); ok {
		t.Fatal("reference escaped account client")
	}
}

func TestMissingUserLookupBoundAndDeadlinePreventMutation(t *testing.T) {
	for _, mode := range []string{"missing", "page_cap", "stuck_cursor", "deadline", "caller_hash"} {
		t.Run(mode, func(t *testing.T) {
			var pages, writes atomic.Int32
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				r := m.Request
				var response proto.Message
				switch r.Method {
				case "GetContacts":
					response = &wire.ContactsResponse{}
				case "LoadDialogs":
					p := pages.Add(1)
					if mode == "deadline" {
						return
					}
					response = &wire.DialogsResponse{}
					if mode == "page_cap" {
						response = &wire.DialogsResponse{Dialogs: dialogPage(100, 100000-int64(p))}
					}
					if mode == "stuck_cursor" {
						response = &wire.DialogsResponse{Dialogs: dialogPage(100, 100000)}
					}
				default:
					writes.Add(1)
					t.Errorf("mutation attempted without a resolved peer: %s", r.Method)
					return
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			c.opts.RequestTimeout = 50 * time.Millisecond
			if mode == "page_cap" {
				c.opts.RequestTimeout = time.Second
			}
			peer := domains.Peer{Type: "user", ID: "42424242"}
			if mode == "caller_hash" {
				peer.AccessHash = "123456789"
			}
			started := time.Now()
			_, err := c.resolvedUserRef(context.Background(), peer)
			if mode == "deadline" {
				if codeOf(err) != "PROVIDER_READ_FAILED" {
					t.Fatal(err)
				}
				if time.Since(started) > time.Second {
					t.Fatal("lookup deadline not bounded")
				}
			} else if codeOf(err) != "PEER_NOT_FOUND" {
				t.Fatal(err)
			}
			if writes.Load() != 0 || pages.Load() > 10 {
				t.Fatal("unbounded or mutating lookup")
			}
			if mode == "page_cap" && pages.Load() != 10 {
				t.Fatalf("unexpected cap %d", pages.Load())
			}
			if mode == "stuck_cursor" && pages.Load() != 2 {
				t.Fatalf("nonadvancing cursor retried %d times", pages.Load())
			}
		})
	}
}

func TestConversationReadsCacheSeparateRefsWithoutExposingHashes(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var response proto.Message
		if r.Method == "LoadHistory" {
			response = &wire.HistoryResponse{Users: []*wire.User{{Id: 44, AccessHash: 555}}, Groups: []*wire.Group{{Id: 79, AccessHash: 444}, {Id: 77, AccessHash: 100}}, UserPeers: []*wire.PeerRef{{Id: 42, AccessHash: 999}}, GroupPeers: []*wire.PeerRef{{Id: 77, AccessHash: 888}}}
		} else {
			q := &wire.DialogsRequest{}
			_ = decode(r.Payload, q)
			if q.MinDate != 0 {
				t.Error("explicit zero cursor changed")
			}
			response = &wire.DialogsResponse{Groups: []*wire.Group{{Id: 78, AccessHash: 100}, {Id: 80, AccessHash: 200, GroupType: 1}}, UserPeers: []*wire.PeerRef{{Id: 43, AccessHash: 777}}, GroupPeers: []*wire.PeerRef{{Id: 78, AccessHash: 666}, {Id: 80, AccessHash: 201}, {Id: 81, AccessHash: 202}}}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	for _, tc := range []struct{ op, body string }{{"chat.history", `{"peer":{"type":"user","id":"42"}}`}, {"chats", `{"date":"0"}`}} {
		result, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
		if err != nil || strings.Contains(string(result), "access_hash") {
			t.Fatalf("%s %v", result, err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.peerHashes["group:80"]; ok {
		t.Fatal("channel ref misclassified as group")
	}
	if _, ok := c.peerHashes["group:81"]; ok {
		t.Fatal("bare untyped group/channel ref misclassified")
	}
	for key, want := range map[string]int64{"user:42": 999, "group:77": 888, "user:43": 777, "group:78": 666, "user:44": 555, "group:79": 444} {
		if c.peerHashes[key] != want {
			t.Fatalf("reference %s not retained", key)
		}
	}
}
