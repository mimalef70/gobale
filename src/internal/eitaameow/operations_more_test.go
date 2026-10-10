package eitaameow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPollCloseResolvesPeerAndPreservesProviderPoll(t *testing.T) {
	for _, kind := range []string{"user", "channel", "group"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			poll := object{"_": "poll", "id": int64(123), "public_voters": true, "multiple_choice": true, "question": "Synthetic?", "answers": []object{{"_": "pollAnswer", "text": "A", "option": []byte{9, 8}}, {"_": "pollAnswer", "text": "B", "option": []byte{7}}}, "close_date": 123456}
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				if calls == 1 {
					expected := "messages.getMessages"
					if kind == "channel" || kind == "group" {
						expected = "channels.getMessages"
						if asObject(p["channel"]).num("access_hash") != 888 {
							t.Error("channel reference not account-owned")
						}
					}
					if method != expected {
						t.Errorf("read: %s", method)
					}
					peer := object{"_": "peerUser", "user_id": int64(43)}
					if kind == "channel" || kind == "group" {
						peer = object{"_": "peerChannel", "channel_id": int64(43)}
					}
					return object{"_": "messages.messages", "messages": []object{{"_": "message", "id": 7, "peer_id": peer, "date": 100, "message": "", "media": object{"_": "messageMediaPoll", "poll": poll, "results": object{"_": "pollResults"}}}}, "chats": []object{}, "users": []object{}}, 200
				}
				if method != "messages.editMessage" || p.num("id") != 7 {
					t.Errorf("edit: %s %v", method, p)
				}
				changed := asObject(asObject(p["media"])["poll"])
				if changed["closed"] != true || changed.str("question") != "Synthetic?" || changed.num("id") != 123 || changed["multiple_choice"] != true || changed["public_voters"] != true || changed.num("close_date") != 123456 || !reflect.DeepEqual(asObjects(changed["answers"])[0]["option"], []byte{9, 8}) {
					t.Errorf("poll fields were not preserved: %v", changed)
				}
				return emptyUpdates(), 200
			})
			c.session.Token = "synthetic"
			c.session.UserID = "42"
			input := object{"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(888)}
			if kind == "channel" || kind == "group" {
				input = object{"_": "inputPeerChannel", "channel_id": int64(43), "access_hash": int64(888)}
			}
			publicID := "43"
			if kind == "group" {
				publicID = "channel_43"
			}
			c.session.Peers = map[string]object{kind + ":" + publicID: input}
			out, e := c.Call(context.Background(), "poll.close", json.RawMessage(`{"peer":{"type":"`+kind+`","id":"`+publicID+`"},"message_id":"7","request_id":"900"}`))
			if e != nil || calls != 2 || !strings.Contains(string(out), "acknowledged") {
				t.Fatalf("close: %s %v calls=%d", out, e, calls)
			}
		})
	}
}
func TestNativeCallRejectsDuplicateKeysBeforeProvider(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("provider contacted"); return nil, 500 })
	_, e := c.Call(context.Background(), "poll.close", json.RawMessage(`{"peer":{"type":"user","id":"43"},"message_id":"7","message_id":"8","request_id":"900"}`))
	if e == nil {
		t.Fatal("duplicate key accepted")
	}
}

func TestPinActivitySearchAndCompletePermissionContracts(t *testing.T) {
	rights := map[string]bool{}
	for _, key := range permissionFields {
		rights[key] = false
	}
	rights["send_messages"] = true
	encodedRights, _ := json.Marshal(rights)
	cases := []struct{ name, body, method string }{
		{"message.pin", `{"peer":{"type":"user","id":"43"},"message_id":"7","silent":true}`, "messages.updatePinnedMessage"},
		{"message.unpin", `{"peer":{"type":"user","id":"43"},"message_id":"7"}`, "messages.updatePinnedMessage"},
		{"chat.activity", `{"peer":{"type":"user","id":"43"},"activity":"typing"}`, "messages.setTyping"},
		{"chat.activity", `{"peer":{"type":"user","id":"43"},"activity":"cancel"}`, "messages.setTyping"},
		{"chat.search", `{"peer":{"type":"user","id":"43"},"query":"Synthetic","limit":1}`, "messages.search"},
		{"group.permissions", `{"peer":{"type":"group","id":"50"},"permissions":` + string(encodedRights) + `}`, "messages.editChatDefaultBannedRights"},
		{"group.member.permissions", `{"peer":{"type":"channel","id":"91"},"user":"43","permissions":` + string(encodedRights) + `}`, "channels.editBanned"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				if method != tt.method {
					t.Error(method)
				}
				switch method {
				case "messages.setTyping":
					expected := "sendMessageTypingAction"
					if strings.Contains(tt.body, "cancel") {
						expected = "sendMessageCancelAction"
					}
					if asObject(p["action"]).str("_") != expected {
						t.Error("wrong typing constructor")
					}
					return object{"_": "boolTrue"}, 200
				case "messages.updatePinnedMessage":
					if p.num("id") != 7 {
						t.Error("wrong pinned message")
					}
					if tt.name == "message.unpin" && p["unpin"] != true {
						t.Error("unpin flag omitted")
					}
				case "messages.search":
					if p.str("q") != "Synthetic" || p.num("offset_id") != 0 || p.num("limit") != 1 {
						t.Error("search filters changed")
					}
					return object{"_": "messages.messages", "messages": []object{{"_": "message", "id": 7, "peer_id": object{"_": "peerUser", "user_id": int64(43)}, "date": 100, "message": "Synthetic"}}, "chats": []object{}, "users": []object{}}, 200
				case "channels.editBanned", "messages.editChatDefaultBannedRights":
					if asObject(p["banned_rights"])["send_messages"] != true || asObject(p["banned_rights"]).num("until_date") != 0 {
						t.Error("rights not sent")
					}
					if method == "channels.editBanned" && asObject(p["participant"]).num("access_hash") != 888 {
						t.Error("member reference not resolved")
					}
				}
				return emptyUpdates(), 200
			})
			c.session.Token = "synthetic"
			c.session.UserID = "42"
			c.session.Peers = map[string]object{"user:43": {"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(888)}, "group:50": {"_": "inputPeerChat", "chat_id": int64(50)}, "channel:91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(777)}}
			raw := strings.TrimSuffix(tt.body, "}") + `,"request_id":"900"}`
			out, e := c.Call(context.Background(), tt.name, json.RawMessage(raw))
			if e != nil || calls != 1 {
				t.Fatalf("call %s %v %d", out, e, calls)
			}
			if tt.name == "chat.search" && !strings.Contains(string(out), `"next_cursor"`) {
				t.Errorf("search cursor: %s", out)
			}
		})
	}
	for _, raw := range []string{`{"peer":{"type":"group","id":"50"},"permissions":{"send_messages":true}}`, `{"peer":{"type":"group","id":"50"},"permissions":` + string(encodedRights) + `,"until":"tomorrow"}`} {
		if _, _, e := (Contract{}).NormalizeOperation("group.permissions", json.RawMessage(raw)); e == nil {
			t.Fatal("partial or malformed rights replacement admitted")
		}
	}
}

func TestOfficialNoSendMethodsAreNotAdmitted(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		t.Fatal("source-blocked method contacted provider")
		return nil, 500
	})
	for _, name := range []string{"send.sticker", "send.gif", "sticker.list", "sticker.get", "sticker.pack.add", "sticker.pack.remove", "gif.list", "contacts.frequent"} {
		if _, e := c.Call(context.Background(), name, json.RawMessage(`{"request_id":"900"}`)); e == nil {
			t.Errorf("%s was admitted despite official no-send evidence", name)
		}
	}
}
