package rubikameow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestCommunicationRemainingFamiliesEncryptedWireAndProjection(t *testing.T) {
	cases := []struct {
		name, method, body, want string
		response                 object
		check                    func(*testing.T, object)
	}{
		{"contacts.online", "getContactsLastOnline", `{"users":["u0test"]}`, `"last_online":"Online"`, object{"users": []object{{"user_guid": "u0test", "last_online": "Online", "online_time": 100, "auth": "private"}}}, func(t *testing.T, p object) {
			if p["user_guids"] == nil || p["user_guds"] != nil {
				t.Fatal("reference typo was used")
			}
		}},
		{"peers.get", "getAbsObjects", `{"peers":[{"type":"group","id":"g0test"}]}`, `"title":"Synthetic"`, object{"abs_objects": []object{{"object_guid": "g0test", "title": "Synthetic", "avatar_thumbnail": object{"access_hash_rec": "private"}}}}, func(t *testing.T, p object) {
			if p["objects_guids"] == nil || p["objects"] != nil {
				t.Fatal("reference's obsolete field used")
			}
		}},
		{"group.common", "getCommonGroups", `{"user_id":"u0test"}`, `"group_guid":"g0test"`, object{"abs_groups": []object{{"group_guid": "g0test", "title": "Synthetic", "access_hash": "private"}}}, nil},
		{"group.online", "getGroupOnlineCount", `{"peer":{"type":"group","id":"g0test"}}`, `"online_count":7`, object{"online_count": 7}, nil},
		{"group.mentions", "getGroupMentionList", `{"peer":{"type":"group","id":"g0test"},"query":"synthetic"}`, `"member_guid":"u0test"`, object{"in_chat_members": []object{{"member_guid": "u0test", "first_name": "Synthetic", "access_hash": "private"}}}, func(t *testing.T, p object) {
			if p.str("search_mention") != "synthetic" {
				t.Fatal("search missing")
			}
		}},
		{"group.mentions", "getGroupMentionList", `{"peer":{"type":"group","id":"g0test"}}`, `"in_chat_members":[]`, object{"in_chat_members": []object{}}, func(t *testing.T, p object) {
			if v, ok := p["search_mention"]; !ok || v != nil {
				t.Fatal("empty search must be null")
			}
		}},
		{"chat.action", "setActionChat", `{"peer":{"type":"user","id":"u0test"},"action":"Mute","duration_seconds":3600}`, `"acknowledged":true`, object{"status": "OK"}, func(t *testing.T, p object) {
			if p.str("action") != "Mute" || p.num("duration") != 3600 {
				t.Fatal("mute duration omitted")
			}
		}},
		{"chat.history.clear", "deleteChatHistory", `{"peer":{"type":"user","id":"u0test"},"last_message_id":"9007199254740993"}`, `"acknowledged":true`, object{"status": "OK"}, func(t *testing.T, p object) {
			if p.str("last_message_id") != "9007199254740993" {
				t.Fatal("history boundary changed")
			}
		}},
		{"chat.remove", "deleteUserChat", `{"peer":{"type":"user","id":"u0test"},"last_message_id":"0"}`, `"acknowledged":true`, object{"status": "OK"}, func(t *testing.T, p object) {
			if p.str("last_deleted_message_id") != "0" || p.str("user_guid") != "u0test" {
				t.Fatal("explicit boundary or user changed")
			}
		}},
		{"group.chat.remove", "deleteNoAccessGroupChat", `{"peer":{"type":"group","id":"g0test"}}`, `"acknowledged":true`, object{"status": "OK"}, nil},
		{"link.resolve", "getLinkFromAppUrl", `{"app_url":"https://rubika.ir/synthetic"}`, `"link_url":"rubika://r.rubika.ir/synthetic"`, object{"link": object{"type": "url", "link_url": "rubika://r.rubika.ir/synthetic", "token": "private"}}, nil},
		{"search.messages", "searchGlobalMessages", `{"query":"synthetic","search_type":"Text","start_id":"cursor-1"}`, `"id":"9007199254740995"`, object{"messages": []object{{"object_guid": "u0test", "message": object{"message_id": "9007199254740995", "time": 100, "text": "synthetic", "type": "Text", "author_object_guid": "u0test", "auth": "private"}}}, "next_start_id": "cursor-2", "has_continue": true}, func(t *testing.T, p object) {
			if p.str("start_id") != "cursor-1" || p.str("type") != "Text" || p.str("search_text") != "synthetic" {
				t.Fatal("search cursor/type changed")
			}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := newRPCFixture(t, func(method string, p object, anonymous bool) object {
				calls++
				if anonymous || method != tt.method {
					t.Fatalf("wrong wire %s", method)
				}
				if tt.check != nil {
					tt.check(t, p)
				}
				return tt.response
			})
			p, _ := jsonObject([]byte(tt.body))
			p["request_id"] = "902"
			raw, _ := json.Marshal(p)
			result, err := c.Call(context.Background(), tt.name, raw)
			if err != nil || calls != 1 || !strings.Contains(string(result), tt.want) || strings.Contains(string(result), "private") {
				t.Fatalf("projection %s calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func TestCommunicationRejectsUnsafeContractsBeforeWire(t *testing.T) {
	cases := []struct{ name, body string }{
		{"contacts.online", `{"users":["u0test","u0test"]}`},
		{"peers.get", `{"peers":[{"type":"group","id":"u0test"}]}`},
		{"group.online", `{"peer":{"type":"channel","id":"c0test"}}`},
		{"group.mentions", `{"peer":{"type":"user","id":"u0test"}}`},
		{"chat.action", `{"peer":{"type":"user","id":"u0test"},"action":"Unmute","duration_seconds":1}`},
		{"chat.action", `{"peer":{"type":"user","id":"u0test"},"action":"Destroy"}`},
		{"chat.action", `{"peer":{"type":"group","id":"g0test"},"action":"Block"}`},
		{"chat.history.clear", `{"peer":{"type":"user","id":"u0test"},"last_message_id":"0"}`},
		{"chat.remove", `{"peer":{"type":"channel","id":"c0test"},"last_message_id":"1"}`},
		{"chat.remove", `{"peer":{"type":"user","id":"u0test"},"last_message_id":"-1"}`},
		{"link.resolve", `{"app_url":"https://rubika.ir/synthetic?token=private"}`},
		{"link.resolve", `{"app_url":"https://user:pass@rubika.ir/synthetic"}`},
		{"link.resolve", `{"app_url":"https://evil.invalid/synthetic"}`},
		{"search.messages", `{"query":"synthetic","search_type":"raw"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := (Contract{}).NormalizeOperation(tt.name, json.RawMessage(tt.body)); err == nil {
				t.Fatalf("unsafe payload admitted: %s", tt.body)
			}
		})
	}
}

func TestCommunicationRejectsUnrequestedAndMalformedProviderResults(t *testing.T) {
	cases := []struct {
		name, body string
		response   object
	}{
		{"contacts.online", `{"users":["u0test"]}`, object{"users": []object{{"user_guid": "u0other"}}}},
		{"peers.get", `{"peers":[{"type":"group","id":"g0test"}]}`, object{"abs_objects": []object{{"object_guid": "g0other"}}}},
		{"group.common", `{"user_id":"u0test"}`, object{"abs_groups": []object{{"group_guid": "u0test"}}}},
		{"group.mentions", `{"peer":{"type":"group","id":"g0test"}}`, object{"in_chat_members": []object{{"member_guid": "g0other"}}}},
		{"group.online", `{"peer":{"type":"group","id":"g0test"}}`, object{"online_count": -1}},
		{"link.resolve", `{"app_url":"https://rubika.ir/synthetic"}`, object{"link": object{"type": "url", "link_url": "rubika://w.rubika.ir/wallet"}}},
		{"search.messages", `{"query":"synthetic","search_type":"Text"}`, object{"messages": []object{{"object_guid": "u0test", "message": object{"object_guid": "u0other", "message_id": "902", "time": 100}}}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newRPCFixture(t, func(string, object, bool) object { return tt.response })
			if out, err := c.Call(context.Background(), tt.name, json.RawMessage(tt.body)); err == nil {
				t.Fatalf("unsafe provider result: %s", out)
			}
		})
	}
}

func TestCommunicationMutationsNeverRetryUnknown(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"chat.action", `{"peer":{"type":"user","id":"u0test"},"action":"Pin","request_id":"902"}`},
		{"chat.history.clear", `{"peer":{"type":"user","id":"u0test"},"last_message_id":"901","request_id":"902"}`},
		{"chat.remove", `{"peer":{"type":"user","id":"u0test"},"last_message_id":"901","request_id":"902"}`},
		{"group.chat.remove", `{"peer":{"type":"group","id":"g0test"},"request_id":"902"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := newRPCFixture(t, func(string, object, bool) object { calls++; return nil })
			_, err := c.Call(context.Background(), tt.name, json.RawMessage(tt.body))
			var de *domains.Error
			if !errors.As(err, &de) || !de.Ambiguous || calls != 1 {
				t.Fatalf("ambiguous mutation %d %v", calls, err)
			}
		})
	}
}

func TestGlobalMessageSearchRegistersOwnedMediaBeforeAdvertisingDownload(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		if method != "searchGlobalMessages" {
			t.Fatal(method)
		}
		return object{"messages": []object{{"object_guid": "u0test", "message": object{"message_id": "902", "time": 100, "type": "FileInline", "file_inline": object{"file_id": "903", "dc_id": "1", "access_hash_rec": "private", "size": 10, "type": "File", "file_name": "fixture.txt", "mime": "txt"}}}}}
	})
	calls := 0
	fail := false
	c.cfg.SaveMediaReference = func(_ context.Context, peer domains.Peer, id string, ref domains.ProviderMedia) (bool, error) {
		calls++
		var private privateMedia
		if peer.ID != "u0test" || id != "902" || json.Unmarshal(ref.Data, &private) != nil || private.Account != "u0self" || private.Hash != "private" {
			t.Fatal("media account/reference binding changed")
		}
		if fail {
			return false, errors.New("synthetic persistence failure")
		}
		return true, nil
	}
	request := json.RawMessage(`{"query":"fixture","search_type":"Text"}`)
	out, err := c.Call(context.Background(), "search.messages", request)
	if err != nil || calls != 1 || !strings.Contains(string(out), `"download_supported":true`) || strings.Contains(string(out), "private") {
		t.Fatalf("media result %s %v", out, err)
	}
	fail = true
	if out, err = c.Call(context.Background(), "search.messages", request); err == nil || len(out) > 0 {
		t.Fatalf("storage failure became a successful downloadable result %s %v", out, err)
	}
}

func TestChatActionsFiniteWireValues(t *testing.T) {
	for _, action := range []string{"Mute", "Unmute", "Pin", "Unpin", "Block", "Unblock", "Archive", "UnArchive"} {
		t.Run(action, func(t *testing.T) {
			c, _ := newRPCFixture(t, func(method string, p object, _ bool) object {
				if method != "setActionChat" || p.str("action") != action || p.str("object_guid") != "u0test" || p["duration"] != nil {
					t.Fatalf("unexpected action wire %#v", p)
				}
				return object{}
			})
			raw, _ := json.Marshal(object{"peer": domains.Peer{Type: "user", ID: "u0test"}, "action": action, "request_id": "902"})
			if _, err := c.Call(context.Background(), "chat.action", raw); err != nil {
				t.Fatal(err)
			}
		})
	}
}
