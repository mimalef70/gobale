package eitaameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func inviteFullChat(id int64) object {
	return object{"_": "messages.chatFull", "full_chat": object{"_": "chatFull", "id": id, "about": "Synthetic", "participants": object{"_": "chatParticipants", "chat_id": id, "participants": []object{}, "version": 1}, "notify_settings": object{"_": "peerNotifySettings"}, "exported_invite": object{"_": "chatInviteExportedLayer122", "link": "https://eitaa.com/joinchat/synthetic"}}, "users": []object{}, "chats": []object{}}
}
func TestGroupLinkReusesBoundExistingInvitationWithoutExportWrite(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls++
		if method != "messages.getFullChat" || p.num("chat_id") != 50 {
			t.Fatalf("unexpected call %s", method)
		}
		return inviteFullChat(50), 200
	})
	mediaClientSource(c, nil)
	c.session.Peers = map[string]object{"group:50": {"_": "inputPeerChat", "chat_id": int64(50)}}
	raw, err := c.Call(context.Background(), "group.link", json.RawMessage(`{"peer":{"type":"group","id":"50"},"request_id":"1234"}`))
	if err != nil || calls != 1 || string(raw) != `{"link":"https://eitaa.com/joinchat/synthetic"}` {
		t.Fatalf("link read failed: %s %v calls=%d", raw, err, calls)
	}
	public, _ := json.Marshal(c.projectOperation(inviteFullChat(50)))
	if strings.Contains(string(public), "joinchat") {
		t.Fatal("ordinary group info leaked its private invitation")
	}
}
func TestGroupLinkRejectsDifferentFullChatBeforeExport(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, _ object) (object, int) {
		if method != "messages.getFullChat" {
			t.Fatal("unexpected export after mismatched peer")
		}
		return inviteFullChat(51), 200
	})
	mediaClientSource(c, nil)
	c.session.Peers = map[string]object{"group:50": {"_": "inputPeerChat", "chat_id": int64(50)}}
	if _, err := c.Call(context.Background(), "group.link", json.RawMessage(`{"peer":{"type":"group","id":"50"},"request_id":"1234"}`)); err == nil {
		t.Fatal("foreign invitation accepted")
	}
}
