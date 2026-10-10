package eitaameow

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func emptyUpdates() object {
	return object{"_": "updates", "updates": []object{}, "users": []object{}, "chats": []object{}, "date": 100, "seq": 1}
}
func TestExtendedMutationContractsUseReviewedWireMethods(t *testing.T) {
	rights := map[string]bool{}
	for _, k := range adminRights {
		rights[k] = false
	}
	rights["change_info"] = true
	rightsJSON, _ := json.Marshal(rights)
	tests := []struct{ name, body, method string }{
		{"contacts.add", `{"user":"43","first_name":"Synthetic"}`, "contacts.addContact"},
		{"contacts.import", `{"contacts":[{"phone":"989000000000","first_name":"Synthetic"}]}`, "contacts.importContacts"},
		{"contacts.remove", `{"user":"43"}`, "contacts.deleteContacts"},
		{"account.block", `{"peer":{"type":"user","id":"43"}}`, "contacts.block"},
		{"account.unblock", `{"peer":{"type":"user","id":"43"}}`, "contacts.unblock"},
		{"account.name", `{"first_name":"Synthetic","last_name":"Fixture"}`, "account.updateProfile"},
		{"account.about", `{"about":"Synthetic profile"}`, "account.updateProfile"},
		{"account.username", `{"username":"synthetic_fixture"}`, "account.updateUsername"},
		{"group.create", `{"title":"Synthetic","users":["43"]}`, "messages.createChat"},
		{"channel.create", `{"title":"Synthetic","megagroup":true}`, "channels.createChannel"},
		{"group.name", `{"peer":{"type":"group","id":"50"},"title":"Synthetic"}`, "messages.editChatTitle"},
		{"group.name", `{"peer":{"type":"channel","id":"91"},"title":"Synthetic"}`, "channels.editTitle"},
		{"group.add", `{"peer":{"type":"group","id":"50"},"user":"43"}`, "messages.addChatUser"},
		{"group.add", `{"peer":{"type":"channel","id":"91"},"user":"43"}`, "channels.inviteToChannel"},
		{"group.remove", `{"peer":{"type":"group","id":"50"},"user":"43"}`, "messages.deleteChatUser"},
		{"group.promote", `{"peer":{"type":"channel","id":"91"},"user":"43","rights":` + string(rightsJSON) + `}`, "channels.editAdmin"},
		{"group.demote", `{"peer":{"type":"channel","id":"91"},"user":"43"}`, "channels.editAdmin"},
		{"group.ban", `{"peer":{"type":"channel","id":"91"},"user":"43"}`, "channels.editBanned"},
		{"group.unban", `{"peer":{"type":"channel","id":"91"},"user":"43"}`, "channels.editBanned"},
		{"group.join_public", `{"peer":{"type":"channel","id":"91"}}`, "channels.joinChannel"},
		{"group.leave", `{"peer":{"type":"channel","id":"91"}}`, "channels.leaveChannel"},
		{"group.leave", `{"peer":{"type":"group","id":"50"}}`, "messages.deleteChatUser"},
		{"group.username", `{"peer":{"type":"channel","id":"91"},"username":"synthetic_fixture"}`, "channels.updateUsername"},
		{"group.join", `{"token":"synthetic_fixture"}`, "messages.importChatInvite"},
		{"folder.set", `{"id":2,"title":"Synthetic","include_peers":[{"type":"user","id":"43"}],"exclude_peers":[]}`, "messages.updateDialogFilter"},
		{"folder.remove", `{"id":2}`, "messages.updateDialogFilter"},
		{"poll.vote", `{"peer":{"type":"user","id":"43"},"message_id":"7","options":["AA=="]}`, "messages.sendVote"},
		{"send.poll", `{"peer":{"type":"user","id":"43"},"question":"Synthetic?","answers":["A","B"]}`, "messages.sendMedia"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.method, func(t *testing.T) {
			count := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				count++
				if method != tt.method {
					t.Errorf("method %s want %s", method, tt.method)
				}
				if p["channel"] != nil && asObject(p["channel"]).num("access_hash") != 777 {
					t.Error("did not resolve account-owned channel hash")
				}
				if method == "channels.editAdmin" && asObject(p["admin_rights"]).str("_") != "chatAdminRights" {
					t.Error("missing rights constructor")
				}
				if method == "messages.sendMedia" {
					if p.num("random_id") != 900 || asObject(asObject(p["media"])["poll"]).num("id") != 900 {
						t.Error("poll identity not derived from persisted request")
					}
					return object{"_": "updateShortSentMessage", "id": 9, "pts": 1, "pts_count": 1, "date": 100, "out": true}, 200
				}
				codec, _ := bundledCodec()
				switch codec.methods[method].Type {
				case "Bool":
					return object{"_": "boolTrue"}, 200
				case "User":
					return object{"_": "user", "id": int64(42), "access_hash": int64(555), "first_name": "Synthetic"}, 200
				case "contacts.ImportedContacts":
					return object{"_": "contacts.importedContacts", "imported": []object{}, "popular_invites": []object{}, "retry_contacts": []int64{}, "users": []object{}}, 200
				case "messages.StickerSetInstallResult":
					return object{"_": "messages.stickerSetInstallResultSuccess"}, 200
				}
				return emptyUpdates(), 200
			})
			c.session.Token = "synthetic"
			c.session.UserID = "42"
			c.session.Peers = map[string]object{"user:43": {"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(888)}, "group:50": {"_": "inputPeerChat", "chat_id": int64(50)}, "channel:91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(777)}}
			body := strings.TrimSuffix(tt.body, "}") + `,"request_id":"900"}`
			out, err := c.Call(context.Background(), tt.name, json.RawMessage(body))
			if err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("unexpected extra provider requests: %d", count)
			}
			if strings.Contains(string(out), "access_hash") || strings.Contains(string(out), "token") {
				t.Fatalf("private reference escaped: %s", out)
			}
		})
	}
}
func TestExtendedContractsRejectUnsafeInputsBeforeWire(t *testing.T) {
	tests := []struct{ name, body string }{
		{"group.promote", `{"peer":{"type":"channel","id":"91"},"user":"43","rights":{"change_info":true}}`},
		{"group.create", `{"title":"Synthetic","users":["43","43"]}`},
		{"group.name", `{"peer":{"type":"user","id":"43"},"title":"Synthetic"}`},
		{"contacts.add", `{"user":"9223372036854775808","first_name":"Synthetic"}`},
		{"folder.set", `{"id":2,"title":"Synthetic","include_peers":[{"type":"user","id":"43","access_hash":"777"}],"exclude_peers":[]}`},
		{"poll.vote", `{"peer":{"type":"user","id":"43"},"message_id":"7","options":["not base64"]}`},
		{"send.poll", `{"peer":{"type":"user","id":"43"},"question":"Synthetic?","answers":["A",""]}`},
		{"message.album", `{"peer":{"type":"user","id":"43"},"items":[{"media_id":"one","kind":"file"},{"media_id":"two","kind":"image"}]}`},
		{"message.reaction", `{"peer":{"type":"user","id":"43"},"message_id":"7","reaction":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := (Contract{}).NormalizeOperation(tt.name, json.RawMessage(tt.body)); err == nil {
				t.Fatal("unsafe or unsupported request admitted")
			}
		})
	}
}
func TestExtendedMutationTransportFailureRemainsUnknown(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(string, object) (object, int) { calls++; return nil, 500 })
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	_, err := c.Call(context.Background(), "account.name", json.RawMessage(`{"first_name":"Synthetic","last_name":"Fixture","request_id":"901"}`))
	var de *domains.Error
	if !errors.As(err, &de) || !de.Ambiguous || calls != 1 {
		t.Fatalf("ambiguous mutation retried or failed: %d %v", calls, err)
	}
}
func TestExtendedProjectionExcludesOpaquePrivateFields(t *testing.T) {
	raw, _ := json.Marshal(projectOperation(object{"_": "updates", "users": []any{object{"id": int64(9223372036854775807), "first_name": "Synthetic", "phone": "private", "access_hash": int64(5)}}, "updates": []any{object{"poll_id": int64(55), "results": object{"total_voters": int64(2), "results": []any{object{"option": []byte{0}, "voters": int64(2)}}}}}, "token": "private", "file_reference": []byte{9}}))
	for _, v := range []string{"private", "access_hash", "file_reference", "\"phone\""} {
		if strings.Contains(string(raw), v) {
			t.Fatalf("private projection %s", raw)
		}
	}
	for _, v := range []string{`"id":"9223372036854775807"`, `"poll_id":"55"`, `"option":"AA=="`, `"total_voters":2`} {
		if !strings.Contains(string(raw), v) {
			t.Fatalf("lost public field %s: %s", v, raw)
		}
	}
}
func TestLiteralChannelDifferenceAndTokenConstructors(t *testing.T) {
	codec, _ := bundledCodec()
	// Independently pinned TL constructor literals, not produced by our encoder.
	for _, tt := range []struct{ hex, method, kind string }{
		{"792325dc", "updates.getState", "eitaa_updates_expire_token"},
		{"752325dc", "updates.getState", "eitaa_token_updating"},
	} {
		wire, _ := hex.DecodeString(tt.hex)
		v, err := codec.decodeResponse(tt.method, wire)
		if err != nil || asObject(v).str("_") != tt.kind {
			t.Fatalf("constructor regression %x %v", wire, err)
		}
	}
	wire := make([]byte, 12)
	binary.LittleEndian.PutUint32(wire, 0x3e11affb)
	binary.LittleEndian.PutUint32(wire[4:], 1)
	binary.LittleEndian.PutUint32(wire[8:], 9)
	v, err := codec.decodeResponse("updates.getChannelDifference", wire)
	if err != nil || asObject(v).num("pts") != 9 || asObject(v)["final"] != true {
		t.Fatalf("channel cursor layout %v %#v", err, v)
	}
}

func TestExtendedReadFamiliesProjectOnlyPublicMetadata(t *testing.T) {
	user := object{"_": "user", "id": int64(43), "first_name": "Synthetic", "access_hash": int64(123), "phone": "private-phone"}
	tests := []struct {
		name, body, method string
		response           object
	}{
		{"contacts.resolve", `{"username":"synthetic"}`, "contacts.resolveUsername", object{"_": "contacts.resolvedPeer", "peer": object{"_": "peerUser", "user_id": int64(43)}, "users": []object{user}, "chats": []object{}}},
		{"account.blocked", `{"limit":50}`, "contacts.getBlocked", object{"_": "contacts.blocked", "blocked": []object{}, "chats": []object{}, "users": []object{user}}},
		{"group.info", `{"peer":{"type":"group","id":"50"}}`, "messages.getFullChat", object{"_": "messages.chatFull", "full_chat": object{"_": "chatFull", "id": int64(50), "about": "Synthetic", "participants": object{"_": "chatParticipants", "chat_id": int64(50), "participants": []object{}, "version": 1}, "notify_settings": object{"_": "peerNotifySettings"}}, "users": []object{user}, "chats": []object{}}},
		{"group.members", `{"peer":{"type":"channel","id":"91"}}`, "channels.getParticipants", object{"_": "channels.channelParticipants", "count": 1, "participants": []object{}, "chats": []object{}, "users": []object{user}}},
		{"group.preview", `{"token":"synthetic"}`, "messages.checkChatInvite", object{"_": "chatInvite", "title": "Synthetic", "photo": object{"_": "photoEmpty", "id": int64(1)}, "participants_count": 1}},
		{"folder.list", `{}`, "messages.getDialogFilters", object{"_": "fixture.vector", "items": []object{{"_": "dialogFilter", "id": 2, "title": "Synthetic", "pinned_peers": []object{}, "include_peers": []object{{"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(123)}}, "exclude_peers": []object{}}}}},
		{"poll.results", `{"peer":{"type":"user","id":"43"},"message_id":"7"}`, "messages.getPollResults", emptyUpdates()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				if method != tt.method {
					t.Errorf("method %s want %s", method, tt.method)
				}
				return tt.response, 200
			})
			c.session.Token = "synthetic"
			c.session.UserID = "42"
			c.session.Peers = map[string]object{"user:43": {"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(123)}, "group:50": {"_": "inputPeerChat", "chat_id": int64(50)}, "channel:91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(5)}}
			out, err := c.Call(context.Background(), tt.name, json.RawMessage(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !json.Valid(out) {
				t.Fatalf("invalid result %d %s", calls, out)
			}
			for _, secret := range []string{"access_hash", "private-phone", "token", "file_reference"} {
				if strings.Contains(string(out), secret) {
					t.Fatalf("private projection %s", out)
				}
			}
			if (tt.name == "sticker.list" || tt.name == "gif.list") && !strings.Contains(string(out), `"9007199254740993"`) {
				t.Fatalf("ID precision lost: %s", out)
			}
		})
	}
}
