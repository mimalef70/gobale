package rubikameow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestExtendedCommunicationEncryptedWire(t *testing.T) {
	cases := []struct{ name, method, body string }{
		{"contacts.add", "addAddressBook", `{"phone":"989000000000","first_name":"Synthetic"}`},
		{"contacts.remove", "deleteContact", `{"user_id":"u0test"}`},
		{"contacts.resolve", "getObjectByUsername", `{"username":"synthetic"}`},
		{"search.global", "searchGlobalObjects", `{"query":"synthetic"}`},
		{"chat.activity", "sendChatActivity", `{"peer":{"type":"user","id":"u0test"},"activity":"Typing"}`},
		{"chat.activity", "sendChatActivity", `{"peer":{"type":"user","id":"u0test"},"activity":"Uploading"}`},
		{"chat.activity", "sendChatActivity", `{"peer":{"type":"user","id":"u0test"},"activity":"Recording"}`},
		{"account.name", "updateProfile", `{"first_name":"Synthetic","last_name":"Fixture"}`},
		{"account.about", "updateProfile", `{"about":"Synthetic"}`},
		{"account.username", "updateUsername", `{"username":"synthetic"}`},
		{"group.create", "addGroup", `{"title":"Synthetic","users":["u0test"]}`},
		{"channel.create", "addChannel", `{"title":"Synthetic","channel_type":"Private","users":["u0test"]}`},
		{"channel.create", "addChannel", `{"title":"Synthetic","description":"Provided","channel_type":"Private","users":["u0test"]}`},
		{"group.name", "editGroupInfo", `{"peer":{"type":"group","id":"g0test"},"title":"Synthetic"}`},
		{"group.description", "editChannelInfo", `{"peer":{"type":"channel","id":"c0test"},"description":"Synthetic"}`},
		{"group.username", "updateChannelUsername", `{"peer":{"type":"channel","id":"c0test"},"username":"synthetic"}`},
		{"group.add", "addGroupMembers", `{"peer":{"type":"group","id":"g0test"},"users":["u0test"]}`},
		{"group.add", "addChannelMembers", `{"peer":{"type":"channel","id":"c0test"},"users":["u0test"]}`},
		{"group.promote", "setGroupAdmin", `{"peer":{"type":"group","id":"g0test"},"user_id":"u0test","mode":"replace","access_list":["SendMessages"]}`},
		{"group.demote", "setChannelAdmin", `{"peer":{"type":"channel","id":"c0test"},"user_id":"u0test"}`},
		{"group.permissions", "getGroupAdminAccessList", `{"peer":{"type":"group","id":"g0test"},"user_id":"u0test"}`},
		{"group.permissions", "getChannelAdminAccessList", `{"peer":{"type":"channel","id":"c0test"},"user_id":"u0test"}`},
		{"group.default_permissions", "getGroupDefaultAccess", `{"peer":{"type":"group","id":"g0test"}}`},
		{"group.default_permissions.set", "setGroupDefaultAccess", `{"peer":{"type":"group","id":"g0test"},"mode":"replace","access_list":["SendMessages"]}`},
		{"group.leave", "leaveGroup", `{"peer":{"type":"group","id":"g0test"}}`},
		{"group.leave", "joinChannelAction", `{"peer":{"type":"channel","id":"c0test"}}`},
		{"group.join_public", "joinChannelAction", `{"peer":{"type":"channel","id":"c0test"}}`},
		{"group.link.revoke", "setGroupLink", `{"peer":{"type":"group","id":"g0test"}}`},
		{"group.link.revoke", "setChannelLink", `{"peer":{"type":"channel","id":"c0test"}}`},
		{"group.join", "joinGroup", `{"peer_type":"group","token":"synthetic"}`},
		{"group.join", "joinChannelByLink", `{"peer_type":"channel","token":"synthetic"}`},
		{"group.preview", "groupPreviewByJoinLink", `{"peer_type":"group","token":"synthetic"}`},
		{"group.preview", "channelPreviewByJoinLink", `{"peer_type":"channel","token":"synthetic"}`},
		{"group.history", "editGroupInfo", `{"peer":{"type":"group","id":"g0test"},"visibility":"Hidden"}`},
		{"folders.add", "addFolder", `{"name":"Synthetic","include_peers":[{"type":"user","id":"u0test"}],"exclude_peers":[]}`},
		{"folders.edit", "editFolder", `{"folder_id":"folder-1","name":"Synthetic","include_peers":[],"exclude_peers":[]}`},
		{"folders.remove", "deleteFolder", `{"folder_id":"folder-1"}`},
		{"poll.results", "getPollStatus", `{"poll_id":"poll-1"}`},
		{"poll.vote", "votePoll", `{"poll_id":"poll-1","selection_index":0}`},
		{"poll.voters", "getPollOptionVoters", `{"poll_id":"poll-1","selection_index":0,"start_id":"cursor-1"}`},
		{"send.poll", "createPoll", `{"peer":{"type":"user","id":"u0test"},"question":"Synthetic?","options":["A","B"],"is_anonymous":true,"allows_multiple_answers":false}`},
		{"sticker.get", "getStickersBySetIDs", `{"sticker_set_id":"set-1"}`},
		{"sticker.pack.add", "actionOnStickerSet", `{"sticker_set_id":"set-1"}`},
		{"sticker.pack.remove", "actionOnStickerSet", `{"sticker_set_id":"set-1"}`},
		{"gif.list", "getMyGifSet", `{}`},
		{"send.location", "sendMessage", `{"peer":{"type":"user","id":"u0test"},"latitude":35.6,"longitude":51.4}`},
		{"send.contact", "sendMessage", `{"peer":{"type":"user","id":"u0test"},"first_name":"Synthetic","phone":"989000000000","user_id":"u0contact"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name+"/"+tt.method, func(t *testing.T) {
			calls := 0
			c, _ := newRPCFixture(t, func(method string, p object, anonymous bool) object {
				calls++
				if method != tt.method || anonymous {
					t.Errorf("unexpected method %s anonymous=%v", method, anonymous)
				}
				if tt.name == "channel.create" {
					var wanted extendedRequest
					_ = json.Unmarshal([]byte(tt.body), &wanted)
					_, present := p["description"]
					if present != (wanted.Description != "") || p.str("description") != wanted.Description {
						t.Error("optional channel description was not preserved")
					}
				}
				if tt.name == "account.about" {
					raw, _ := json.Marshal(p["updated_parameters"])
					if p.str("bio") != "Synthetic" || string(raw) != `["bio"]` || p["first_name"] != nil {
						t.Errorf("omitted profile fields overwritten: %#v", p)
					}
				}
				if tt.name == "group.promote" && p.str("action") != "SetAdmin" {
					t.Error("wrong promotion action")
				}
				if tt.name == "group.demote" && (p.str("action") != "UnsetAdmin" || p["access_list"] != nil) {
					t.Error("demotion must be explicit")
				}
				if tt.name == "send.location" && p.str("type") != "Location" {
					t.Error("location message type omitted")
				}
				if tt.name == "chat.activity" {
					var expected extendedRequest
					_ = json.Unmarshal([]byte(tt.body), &expected)
					if p.str("object_guid") != expected.Peer.ID || p.str("activity") != expected.Activity {
						t.Errorf("activity fields differ from reviewed wire: %#v", p)
					}
				}
				if strings.HasPrefix(tt.name, "send.") {
					if p.str("rnd") != "9007199254740993" {
						t.Errorf("persisted nonce lost: %#v", p)
					}
					return fixtureSent("u0test")
				}
				return object{"status": "OK", "user": object{"user_guid": "u0test", "auth": "private-auth", "access_hash": "private-hash"}, "folder": object{"folder_id": "9007199254740993", "name": "Synthetic"}, "access_list": []string{"SendMessages"}, "poll_status": object{"total_vote": 2}, "token": "private-token"}
			})
			var b map[string]any
			_ = json.Unmarshal([]byte(tt.body), &b)
			b["request_id"] = "9007199254740993"
			raw, _ := json.Marshal(b)
			result, err := c.Call(context.Background(), tt.name, raw)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !json.Valid(result) {
				t.Fatalf("unexpected native result %d %s", calls, result)
			}
			if strings.Contains(string(result), "private-") || strings.Contains(string(result), "access_hash") {
				t.Fatalf("private projection %s", result)
			}
			if strings.HasPrefix(tt.name, "send.") && !strings.Contains(string(result), `"9007199254740995"`) {
				t.Fatalf("ID precision lost: %s", result)
			}
		})
	}
}
func fixtureSent(peer string) object {
	return object{"status": "OK", "message_update": object{"object_guid": peer, "message_id": "9007199254740995", "message": object{"message_id": "9007199254740995", "time": 100}}}
}
func TestExtendedContractsRejectUnsafeBeforeWire(t *testing.T) {
	cases := []struct{ name, body string }{
		{"group.create", `{"title":"Synthetic","users":["u0test","u0test"]}`},
		{"group.promote", `{"peer":{"type":"group","id":"g0test"},"user_id":"u0test","access_list":["SendMessages"]}`},
		{"group.promote", `{"peer":{"type":"group","id":"g0test"},"user_id":"u0test","mode":"replace","access_list":["AcceptOwner"]}`},
		{"group.default_permissions.set", `{"peer":{"type":"channel","id":"c0test"},"mode":"replace","access_list":[]}`},
		{"group.join_public", `{"peer":{"type":"group","id":"g0test"}}`},
		{"folders.add", `{"name":"Synthetic","include_peers":[{"type":"user","id":"c0test"}],"exclude_peers":[]}`},
		{"contacts.remove", `{"user_id":"c0test"}`},
		{"send.location", `{"peer":{"type":"user","id":"u0test"},"latitude":91,"longitude":0}`},
		{"send.poll", `{"peer":{"type":"user","id":"u0test"},"question":"Synthetic?","options":["A",""],"is_anonymous":true,"allows_multiple_answers":false}`},
		{"send.sticker", `{"peer":{"type":"user","id":"u0test"},"sticker_id":"1","sticker_set_id":"2","access_hash":"private"}`},
		{"poll.voters", `{"poll_id":"x","selection_index":-1}`},
		{"poll.close", `{"poll_id":"x"}`},
		{"chat.activity", `{"peer":{"type":"user","id":"u0test"},"activity":"Cancel"}`},
		{"chat.activity", `{"peer":{"type":"user","id":"u0test"},"activity":"is typing >>>"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := (Contract{}).NormalizeOperation(tt.name, json.RawMessage(tt.body)); err == nil {
				t.Fatal("unsafe or unsupported request admitted")
			}
		})
	}
}
func TestExtendedFreshProviderAssetResolution(t *testing.T) {
	for _, kind := range []string{"sticker", "gif"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			c, _ := newRPCFixture(t, func(method string, p object, anonymous bool) object {
				calls++
				if calls == 1 {
					if kind == "sticker" {
						if method != "getStickersBySetIDs" {
							t.Error("missing lookup")
						}
						return object{"stickers": []object{{"sticker_id": "asset", "sticker_set_id": "pack", "access_hash_rec": "private-owned"}}}
					}
					if method != "getMyGifSet" {
						t.Error("missing lookup")
					}
					return object{"gifs": []object{{"file_id": "asset", "access_hash_rec": "private-owned", "type": "Gif"}}}
				}
				if method != "sendMessage" || p.str("rnd") != "902" {
					t.Error("incorrect durable send")
				}
				field := "sticker"
				if kind == "gif" {
					field = "file_inline"
				}
				if asObject(p[field]).str("access_hash_rec") != "private-owned" {
					t.Error("resolved reference lost")
				}
				return fixtureSent("u0test")
			})
			b := object{"peer": domains.Peer{Type: "user", ID: "u0test"}, "request_id": "902"}
			if kind == "sticker" {
				b["sticker_id"], b["sticker_set_id"] = "asset", "pack"
			} else {
				b["file_id"] = "asset"
			}
			raw, _ := json.Marshal(b)
			out, err := c.Call(context.Background(), "send."+kind, raw)
			if err != nil || calls != 2 || strings.Contains(string(out), "private") {
				t.Fatalf("asset flow %d %s %v", calls, out, err)
			}
		})
	}
}
func TestExtendedWriteTransportFailureRemainsUnknown(t *testing.T) {
	calls := 0
	c, _ := newRPCFixture(t, func(string, object, bool) object { calls++; return nil })
	_, err := c.Call(context.Background(), "group.create", json.RawMessage(`{"title":"Synthetic","users":["u0test"],"request_id":"902"}`))
	var de *domains.Error
	if !errors.As(err, &de) || !de.Ambiguous || calls != 1 {
		t.Fatalf("mutation retried or conclusively failed %d %v", calls, err)
	}
}
func TestExtendedEventProjectionExcludesOpaqueData(t *testing.T) {
	for _, tt := range []struct {
		key   string
		value object
	}{
		{"poll", object{"poll_id": "poll-1", "question": "Synthetic?", "options": []any{"A", "B"}, "auth": "private"}},
		{"location", object{"latitude": json.Number("35.6"), "longitude": json.Number("51.4"), "raw": "private"}},
		{"contact_message", object{"first_name": "Synthetic", "phone_number": "989000000000", "user_guid": "u0test", "access_hash": "private"}},
		{"sticker", object{"sticker_id": "asset", "sticker_set_id": "pack", "access_hash_rec": "private", "url": "https://private.invalid"}},
	} {
		t.Run(tt.key, func(t *testing.T) {
			msg := &domains.Message{}
			out, handled, err := projectExtendedMessage(object{tt.key: tt.value}, msg)
			b, _ := json.Marshal(out)
			if err != nil || !handled || !msg.Supported || strings.Contains(string(b), "private") {
				t.Fatalf("unsafe projection %#v %s %v", msg, b, err)
			}
		})
	}
}

func TestScopedSearchAndMessageLookupRejectCrossPeerResults(t *testing.T) {
	crossPeer := false
	c, _ := newRPCFixture(t, func(method string, p object, _ bool) object {
		if p.str("object_guid") != "u0test" {
			t.Error("account peer scope lost")
		}
		if method == "searchChatMessages" {
			if p.str("search_text") != "synthetic" || p.str("type") != "Text" {
				t.Error("search fields changed")
			}
			return object{"message_ids": []string{"9007199254740995"}}
		}
		if method != "getMessagesByID" {
			t.Errorf("unexpected method %s", method)
		}
		peer := "u0test"
		if crossPeer {
			peer = "u0other"
		}
		return object{"messages": []object{{"message_id": "9007199254740995", "object_guid": peer, "author_object_guid": "u0test", "text": "synthetic", "type": "Text", "time": 100}}}
	})
	out, err := c.Call(context.Background(), "chat.search", json.RawMessage(`{"peer":{"type":"user","id":"u0test"},"query":"synthetic","search_type":"Text"}`))
	if err != nil || !strings.Contains(string(out), `"9007199254740995"`) {
		t.Fatalf("search %s %v", out, err)
	}
	payload := json.RawMessage(`{"peer":{"type":"user","id":"u0test"},"message_ids":["9007199254740995"]}`)
	out, err = c.Call(context.Background(), "chat.get_by_ids", payload)
	if err != nil || !strings.Contains(string(out), "synthetic") {
		t.Fatalf("lookup %s %v", out, err)
	}
	crossPeer = true
	if _, err = c.Call(context.Background(), "chat.get_by_ids", payload); err == nil {
		t.Fatal("another peer's message rebound to selected peer")
	}
}
func TestMissingProviderAssetCannotReachSend(t *testing.T) {
	calls := 0
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		calls++
		if method != "getStickersBySetIDs" {
			t.Fatal("unresolved asset reached send")
		}
		return object{"stickers": []object{{"sticker_id": "other", "sticker_set_id": "pack", "access_hash_rec": "private"}}}
	})
	_, err := c.Call(context.Background(), "send.sticker", json.RawMessage(`{"peer":{"type":"user","id":"u0test"},"sticker_id":"missing","sticker_set_id":"pack","request_id":"902"}`))
	var de *domains.Error
	if !errors.As(err, &de) || de.Code != "MEDIA_NOT_FOUND" || calls != 1 {
		t.Fatalf("missing asset not rejected: %d %v", calls, err)
	}
}
