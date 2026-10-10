package eitaameow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func moderationFixture(c *Client) {
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	c.cfg.ConnectionID = "connection-A"
	c.session.Peers = map[string]object{"user:43": {"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(888)}, "group:50": {"_": "inputPeerChat", "chat_id": int64(50)}, "channel:91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(777)}, "group:channel_92": {"_": "inputPeerChannel", "channel_id": int64(92), "access_hash": int64(666)}}
}
func inviteFixture(link string) object {
	return object{"_": "chatInviteExported", "link": link, "admin_id": int64(42), "date": 100, "usage": 2}
}
func discussionGroupFixture() object {
	return object{"_": "channel", "id": int64(92), "access_hash": int64(666), "title": "Synthetic", "photo": object{"_": "chatPhotoEmpty"}, "date": 100, "megagroup": true}
}
func moderationCallRaw(body string) json.RawMessage {
	var p object
	_ = json.Unmarshal([]byte(body), &p)
	p["request_id"] = "902"
	raw, _ := json.Marshal(p)
	return raw
}
func TestModerationReviewedWireAndPublicProjection(t *testing.T) {
	cases := []struct{ name, method, body, want string }{
		{"group.participant", "channels.getParticipant", `{"peer":{"type":"channel","id":"91"},"participant":{"type":"user","id":"43"}}`, `"role":"member"`},
		{"group.discussion.candidates", "channels.getGroupsForDiscussion", `{}`, `"id":"channel_92"`},
		{"group.discussion.set", "channels.setDiscussionGroup", `{"peer":{"type":"channel","id":"91"},"group_peer":{"type":"group","id":"channel_92"}}`, `"acknowledged":true`},
		{"group.discussion.unlink", "channels.setDiscussionGroup", `{"peer":{"type":"channel","id":"91"}}`, `"acknowledged":true`},
		{"group.slowmode", "channels.toggleSlowMode", `{"peer":{"type":"group","id":"channel_92"},"seconds":30}`, `"acknowledged":true`},
		{"group.history_visibility", "channels.togglePreHistoryHidden", `{"peer":{"type":"group","id":"channel_92"},"hidden":false}`, `"acknowledged":true`},
		{"group.upgrade", "messages.migrateChat", `{"peer":{"type":"group","id":"50"}}`, `"peer":{"type":"group","id":"channel_92"}`},
		{"group.discussion.message", "messages.getDiscussionMessage", `{"peer":{"type":"channel","id":"91"},"message_id":"12"}`, `"type":"group","id":"channel_92"`},
		{"group.discussion.read", "messages.readDiscussion", `{"peer":{"type":"channel","id":"91"},"message_id":"12","read_max_id":"15"}`, `"acknowledged":true`},
		{"chat.archive", "folders.editPeerFolders", `{"peer":{"type":"user","id":"43"},"archived":true}`, `"acknowledged":true`},
		{"group.invites", "messages.getExportedChatInvites", `{"peer":{"type":"channel","id":"91"}}`, `"link":"synthetic-link"`},
		{"group.invite.get", "messages.getExportedChatInvite", `{"peer":{"type":"channel","id":"91"},"link":"synthetic-link"}`, `"link":"synthetic-link"`},
		{"group.invite.create", "messages.exportChatInvite", `{"peer":{"type":"channel","id":"91"},"usage_limit":5}`, `"acknowledged":true`},
		{"group.invite.edit", "messages.editExportedChatInvite", `{"peer":{"type":"channel","id":"91"},"link":"synthetic-link","expire_date":200}`, `"acknowledged":true`},
		{"group.invite.revoke", "messages.editExportedChatInvite", `{"peer":{"type":"channel","id":"91"},"link":"synthetic-link"}`, `"new_invite"`},
		{"group.invite.delete", "messages.deleteExportedChatInvite", `{"peer":{"type":"channel","id":"91"},"link":"synthetic-link"}`, `"acknowledged":true`},
		{"group.invites.delete_revoked", "messages.deleteRevokedExportedChatInvites", `{"peer":{"type":"channel","id":"91"},"admin_id":"43"}`, `"acknowledged":true`},
		{"group.invites.admins", "messages.getAdminsWithInvites", `{"peer":{"type":"channel","id":"91"}}`, `"admin_id":"42"`},
		{"group.invite.importers", "messages.getChatInviteImporters", `{"peer":{"type":"channel","id":"91"},"link":"synthetic-link"}`, `"user_id":"43"`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				if method != tt.method {
					t.Fatalf("wire %s want %s", method, tt.method)
				}
				if ch := asObject(p["channel"]); ch != nil && ch.num("access_hash") != 777 && ch.num("access_hash") != 666 {
					t.Fatal("caller or missing channel hash")
				}
				switch tt.name {
				case "group.participant":
					if asObject(p["participant"]).num("access_hash") != 888 {
						t.Fatal("participant scope")
					}
					return object{"_": "channels.channelParticipant", "participant": object{"_": "channelParticipant", "user_id": int64(43), "date": 100}, "chats": []object{}, "users": []object{}}, 200
				case "group.discussion.candidates":
					return object{"_": "messages.chats", "chats": []object{discussionGroupFixture()}}, 200
				case "group.discussion.set":
					if asObject(p["broadcast"]).num("channel_id") != 91 || asObject(p["group"]).num("channel_id") != 92 || asObject(p["group"]).num("access_hash") != 666 {
						t.Fatal("discussion references")
					}
				case "group.discussion.unlink":
					if asObject(p["group"]).str("_") != "inputChannelEmpty" {
						t.Fatal("unlink must be explicit empty channel")
					}
				case "group.slowmode":
					if p.num("seconds") != 30 {
						t.Fatal("slowmode seconds")
					}
				case "group.history_visibility":
					if p["enabled"] != false {
						t.Fatal("history visibility inverted")
					}
				case "group.upgrade":
					u := emptyUpdates()
					u["chats"] = []object{discussionGroupFixture()}
					return u, 200
				case "chat.archive":
					if asObjects(p["folder_peers"])[0].num("folder_id") != 1 {
						t.Fatal("archive folder")
					}
				case "group.discussion.message":
					m := historyMessage(12, 100, 43)
					m["peer_id"] = object{"_": "peerChannel", "channel_id": int64(92)}
					return object{"_": "messages.discussionMessage", "messages": []object{m}, "unread_count": 1, "max_id": 15, "chats": []object{}, "users": []object{}}, 200
				case "group.discussion.read":
					if p.num("read_max_id") != 15 || p.num("msg_id") != 12 {
						t.Fatal("discussion read boundary")
					}
				case "group.invites":
					if asObject(p["admin_id"]).str("_") != "inputUserSelf" {
						t.Fatal("default admin must be selected account")
					}
					return object{"_": "messages.exportedChatInvites", "count": 1, "invites": []object{inviteFixture("synthetic-link")}, "users": []object{}}, 200
				case "group.invite.get":
					return object{"_": "messages.exportedChatInvite", "invite": inviteFixture("synthetic-link"), "users": []object{}}, 200
				case "group.invite.create":
					if p.num("usage_limit") != 5 || p["legacy_revoke_permanent"] != nil || p["expire_date"] != nil {
						t.Fatal("unrequested invite fields mutated")
					}
					return inviteFixture("synthetic-link"), 200
				case "group.invite.edit":
					if p.num("expire_date") != 200 || p["usage_limit"] != nil {
						t.Fatal("omitted invite field changed")
					}
					return object{"_": "messages.exportedChatInvite", "invite": inviteFixture("synthetic-link"), "users": []object{}}, 200
				case "group.invite.revoke":
					if p["revoked"] != true {
						t.Fatal("revoke missing")
					}
					return object{"_": "messages.exportedChatInviteReplaced", "invite": inviteFixture("synthetic-link"), "new_invite": inviteFixture("synthetic-replacement"), "users": []object{}}, 200
				case "group.invites.delete_revoked":
					if asObject(p["admin_id"]).num("access_hash") != 888 {
						t.Fatal("admin hash not resolved")
					}
				case "group.invites.admins":
					return object{"_": "messages.chatAdminsWithInvites", "admins": []object{{"_": "chatAdminWithInvites", "admin_id": int64(42), "invites_count": 2, "revoked_invites_count": 1}}, "users": []object{}}, 200
				case "group.invite.importers":
					if asObject(p["offset_user"]).str("_") != "inputUserEmpty" || p.num("offset_date") != 0 {
						t.Fatal("invented importer offset")
					}
					return object{"_": "messages.chatInviteImporters", "count": 1, "importers": []object{{"_": "chatInviteImporter", "user_id": int64(43), "date": 100}}, "users": []object{}}, 200
				}
				codec, _ := bundledCodec()
				if codec.methods[method].Type == "Bool" {
					return object{"_": "boolTrue"}, 200
				}
				return emptyUpdates(), 200
			})
			moderationFixture(c)
			out, err := c.Call(context.Background(), tt.name, moderationCallRaw(tt.body))
			if err != nil || calls != 1 || !strings.Contains(string(out), tt.want) || strings.Contains(string(out), "access_hash") {
				t.Fatalf("out=%s err=%v calls=%d", out, err, calls)
			}
		})
	}
}
func TestModerationUnsafeRequestsRejectedBeforeProvider(t *testing.T) {
	cases := []struct{ name, body string }{
		{"group.discussion.set", `{"peer":{"type":"group","id":"channel_92"},"group_peer":{"type":"group","id":"50"}}`},
		{"group.discussion.set", `{"peer":{"type":"channel","id":"91"},"group_peer":{"type":"user","id":"43"}}`},
		{"group.participant", `{"peer":{"type":"channel","id":"91"},"participant":{"type":"user","id":"43","access_hash":"888"}}`},
		{"group.slowmode", `{"peer":{"type":"group","id":"channel_92"},"seconds":-1}`},
		{"group.history_visibility", `{"peer":{"type":"channel","id":"91"},"hidden":true}`},
		{"group.invite.edit", `{"peer":{"type":"group","id":"50"},"link":"synthetic"}`},
		{"group.invite.get", `{"peer":{"type":"user","id":"43"},"link":"synthetic"}`},
		{"group.invites", `{"peer":{"type":"group","id":"50"},"limit":101}`},
		{"group.discussion.read", `{"peer":{"type":"channel","id":"91"},"message_id":"12","read_max_id":"2147483648"}`},
	}
	for _, tt := range cases {
		if _, _, err := (Contract{}).NormalizeOperation(tt.name, json.RawMessage(tt.body)); err == nil {
			t.Fatalf("unsafe request admitted %s %s", tt.name, tt.body)
		}
	}
	calls := 0
	c, _ := nativeFixture(t, func(string, object) (object, int) { calls++; return nil, 500 })
	moderationFixture(c)
	_, err := c.Call(context.Background(), "group.discussion.set", moderationCallRaw(`{"peer":{"type":"channel","id":"91"},"group_peer":{"type":"group","id":"50"}}`))
	if err == nil || calls != 0 {
		t.Fatal("classic group silently migrated")
	}
}
func TestModerationInvitationCursorBoundToAccountPeerAndFilter(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(_ string, p object) (object, int) {
		calls++
		if calls == 2 && (p.num("offset_date") != 100 || p.str("offset_link") != "first") {
			t.Fatal("cursor offset changed")
		}
		return object{"_": "messages.exportedChatInvites", "count": 2, "invites": []object{inviteFixture("first")}, "users": []object{}}, 200
	})
	moderationFixture(c)
	body := object{"peer": domains.Peer{Type: "channel", ID: "91"}, "limit": 1}
	raw, _ := json.Marshal(body)
	out, err := c.Call(context.Background(), "group.invites", raw)
	if err != nil {
		t.Fatal(err)
	}
	var result object
	_ = json.Unmarshal(out, &result)
	body["cursor"] = result.str("next_cursor")
	if body["cursor"] == "" {
		t.Fatal("missing cursor")
	}
	raw, _ = json.Marshal(body)
	if _, err = c.Call(context.Background(), "group.invites", raw); err == nil {
		t.Fatal("non-advancing cursor accepted")
	}
	body["revoked"] = true
	raw, _ = json.Marshal(body)
	if _, err = c.Call(context.Background(), "group.invites", raw); err == nil {
		t.Fatal("cursor rebound to different filter")
	}
	delete(body, "revoked")
	body["peer"] = domains.Peer{Type: "group", ID: "50"}
	raw, _ = json.Marshal(body)
	if _, err = c.Call(context.Background(), "group.invites", raw); err == nil {
		t.Fatal("cursor rebound to peer")
	}
	body["peer"] = domains.Peer{Type: "channel", ID: "91"}
	c.cfg.ConnectionID = "connection-B"
	raw, _ = json.Marshal(body)
	if _, err = c.Call(context.Background(), "group.invites", raw); err == nil {
		t.Fatal("cursor rebound to connection")
	}
	if calls != 2 {
		t.Fatalf("invalid cursors contacted provider %d", calls)
	}
}
func TestBulkUnpinJournalsEachBatchAndStopsOnAmbiguity(t *testing.T) {
	for _, mode := range []string{"success", "failure", "journal", "limit"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			stages := []domains.OperationStage{}
			c, _ := nativeFixture(t, func(method string, _ object) (object, int) {
				if method != "messages.unpinAllMessages" {
					t.Fatal(method)
				}
				calls++
				if len(stages) == 0 || stages[len(stages)-1].State != "started" || stages[len(stages)-1].Number != calls {
					t.Fatal("provider contacted before stage started")
				}
				if mode == "failure" && calls == 2 {
					return nil, 500
				}
				offset := 0
				if calls == 1 || mode == "limit" {
					offset = 1
				}
				return object{"_": "messages.affectedHistory", "pts": calls, "pts_count": 1, "offset": offset}, 200
			})
			moderationFixture(c)
			ctx := domains.WithOperationStageRecorder(context.Background(), func(_ context.Context, s domains.OperationStage) error {
				if mode == "journal" && s.Number == 2 && s.State == "started" {
					return errors.New("journal unavailable")
				}
				stages = append(stages, s)
				return nil
			})
			out, err := c.Call(ctx, "message.unpin_all", moderationCallRaw(`{"peer":{"type":"group","id":"50"}}`))
			if mode == "success" {
				if err != nil || calls != 2 || !strings.Contains(string(out), `"batches":2`) {
					t.Fatalf("unpin %s %v", out, err)
				}
			} else {
				var de *domains.Error
				if !errors.As(err, &de) || !de.Ambiguous {
					t.Fatalf("partial result not unknown: %v", err)
				}
			}
			if mode == "journal" && calls != 1 || mode == "failure" && calls != 2 || mode == "limit" && calls != 32 {
				t.Fatalf("unexpected retry/count %d", calls)
			}
		})
	}
}
func TestModerationWrongIdentityAndUncertainMutationCannotSucceed(t *testing.T) {
	for _, name := range []string{"group.participant", "group.invite.edit", "group.discussion.unlink"} {
		t.Run(name, func(t *testing.T) {
			c, _ := nativeFixture(t, func(string, object) (object, int) {
				switch name {
				case "group.participant":
					return object{"_": "channels.channelParticipant", "participant": object{"_": "channelParticipant", "user_id": int64(99), "date": 100}, "chats": []object{}, "users": []object{}}, 200
				case "group.invite.edit":
					return object{"_": "messages.exportedChatInvite", "invite": inviteFixture("other"), "users": []object{}}, 200
				}
				return object{"_": "boolFalse"}, 200
			})
			moderationFixture(c)
			body := `{"peer":{"type":"channel","id":"91"},"participant":{"type":"user","id":"43"}}`
			if name == "group.invite.edit" {
				body = `{"peer":{"type":"channel","id":"91"},"link":"selected","usage_limit":1}`
			}
			if name == "group.discussion.unlink" {
				body = `{"peer":{"type":"channel","id":"91"}}`
			}
			_, err := c.Call(context.Background(), name, moderationCallRaw(body))
			if err == nil {
				t.Fatal("wrong response succeeded")
			}
			if name != "group.participant" {
				var de *domains.Error
				if !errors.As(err, &de) || !de.Ambiguous {
					t.Fatal("mutation incorrectly failed conclusively")
				}
			}
		})
	}
}
