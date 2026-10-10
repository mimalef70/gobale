package eitaameow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestPeerNamespacesPreserveCoexistingClassicAndSupergroup(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "classic_first", true: "supergroup_first"}[reverse], func(t *testing.T) {
			c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("unexpected RPC"); return nil, 500 })
			c.session.UserID = "42"
			c.session.Token = "synthetic"
			saves := 0
			c.SetSessionPersister(func(context.Context, *domains.Session) error { saves++; return nil })
			classic := object{"_": "chat", "id": int64(91)}
			supergroup := object{"_": "channel", "id": int64(91), "access_hash": int64(888), "megagroup": true}
			entities := []any{classic, supergroup}
			if reverse {
				entities = []any{supergroup, classic}
			}
			if e := c.rememberEntities(context.Background(), object{"chats": entities}); e != nil {
				t.Fatal(e)
			}
			if saves != 1 || len(c.session.Peers) != 2 {
				t.Fatalf("cache=%#v saves=%d", c.session.Peers, saves)
			}
			for _, id := range []string{"91", "channel_91"} {
				p := domains.Peer{Type: "group", ID: id}
				ref, e := c.inputPeer(context.Background(), p)
				if e != nil {
					t.Fatal(e)
				}
				expected := "inputPeerChat"
				rawPeer := object{"_": "peerChat", "chat_id": int64(91)}
				if id == "channel_91" {
					expected = "inputPeerChannel"
					rawPeer = object{"_": "peerChannel", "channel_id": int64(91)}
				}
				if ref.str("_") != expected {
					t.Fatalf("rerouted %s: %#v", id, ref)
				}
				event, e := c.projectMessage(object{"_": "message", "id": 12, "peer_id": rawPeer, "date": 100, "message": "Synthetic"}, "message")
				if e != nil || event.Peer != p {
					t.Fatalf("projection mixed namespaces: %#v %v", event.Peer, e)
				}
				plain := plainPeer(p)
				if plain.str("_") != rawPeer.str("_") {
					t.Fatalf("upload namespace mismatch %#v", plain)
				}
				cursor := c.encodeCursor(pageCursor{Kind: "history", Peer: p.Key(), ID: 12, Date: 100})
				other := domains.Peer{Type: "group", ID: "91"}
				if id == "91" {
					other.ID = "channel_91"
				}
				if _, e = c.decodeCursor(cursor, "history", other.Key()); e == nil {
					t.Fatal("history cursor crossed namespace")
				}
			}
			raw, _ := json.Marshal(c.session)
			if _, e := decodeSession(&domains.Session{Provider: domains.ProviderEitaa, Version: 1, UserID: "42", Data: raw}); e != nil {
				t.Fatalf("scoped session restore failed: %v", e)
			}
		})
	}
}
func TestPeerNamespaceRejectsCorruptCacheWithoutRerouting(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("corrupt reference contacted provider"); return nil, 500 })
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	c.session.Peers = map[string]object{"group:channel_91": {"_": "inputPeerChat", "chat_id": int64(91)}}
	c.SetSessionPersister(func(context.Context, *domains.Session) error { t.Fatal("collision persisted"); return nil })
	e := c.rememberEntities(context.Background(), object{"chats": []any{object{"_": "channel", "id": int64(91), "access_hash": int64(888), "megagroup": true}}})
	var de *domains.Error
	if !errors.As(e, &de) || de.Code != "PEER_NAMESPACE_CONFLICT" || c.session.Peers["group:channel_91"].str("_") != "inputPeerChat" {
		t.Fatalf("collision not rejected: %v", e)
	}
	if status := c.Status(); status.Recovery != "gap_detected" {
		t.Fatalf("no gap diagnostic: %#v", status)
	}
	if _, e = c.inputPeer(context.Background(), domains.Peer{Type: "group", ID: "channel_91"}); e == nil {
		t.Fatal("corrupt input peer accepted")
	}
	raw, _ := json.Marshal(c.session)
	if _, e = decodeSession(&domains.Session{Provider: domains.ProviderEitaa, Version: 1, UserID: "42", Data: raw}); e == nil {
		t.Fatal("corrupt namespace restored")
	}
}
func TestPeerNamespaceCanonicalValidationAndReferenceRefresh(t *testing.T) {
	for _, p := range []domains.Peer{{Type: "group", ID: "channel_0"}, {Type: "group", ID: "channel_091"}, {Type: "group", ID: "channel_9223372036854775808"}, {Type: "user", ID: "channel_91"}, {Type: "channel", ID: "channel_91"}} {
		if (Contract{}).ValidatePeer(p) == nil {
			t.Fatalf("invalid namespace accepted: %#v", p)
		}
	}
	c, _ := nativeFixture(t, func(string, object) (object, int) { return nil, 500 })
	c.session.UserID = "42"
	c.session.Token = "synthetic"
	c.session.Peers = map[string]object{"group:channel_91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(1)}}
	saves := 0
	c.SetSessionPersister(func(context.Context, *domains.Session) error { saves++; return nil })
	if e := c.rememberEntities(context.Background(), object{"chats": []any{object{"_": "channel", "id": int64(91), "access_hash": int64(2), "megagroup": true}}}); e != nil || saves != 1 || c.session.Peers["group:channel_91"].num("access_hash") != 2 {
		t.Fatalf("valid reference refresh failed %v", e)
	}
	out := c.projectOperation(object{"_": "messages.chatFull", "full_chat": object{"_": "channelFull", "id": int64(91)}, "chats": []any{object{"_": "channel", "id": int64(91), "megagroup": true}}})
	raw, _ := json.Marshal(out)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	full := body["full_chat"].(map[string]any)
	if full["id"] != "channel_91" {
		t.Fatalf("full entity projection leaked raw peer ID: %s", raw)
	}
}
