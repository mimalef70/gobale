package eitaameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProfileAndContactReadContracts(t *testing.T) {
	tests := []struct {
		name, body, method string
		response           object
		want               string
	}{
		{"users.get", `{"user":"43"}`, "users.getFullUser", object{"_": "userFull", "user": object{"_": "user", "id": int64(43), "access_hash": int64(888), "first_name": "Synthetic"}, "about": "Synthetic about", "settings": object{"_": "peerSettings"}, "notify_settings": object{"_": "peerNotifySettings"}, "common_chats_count": 1}, "Synthetic about"},
		{"account.username.check", `{"username":"synthetic"}`, "account.checkUsername", object{"_": "boolFalse"}, `"available":false`},
		{"contacts.ids", `{}`, "contacts.getContactIDs", object{"_": "fixture.vector", "items": []int64{43, 44}}, `"43"`},
		{"contacts.status", `{}`, "contacts.getStatuses", object{"_": "fixture.vector", "items": []object{{"_": "contactStatus", "user_id": int64(9007199254740993), "status": object{"_": "userStatusOffline", "was_online": 100}}}}, `"9007199254740993"`},
		{"contacts.saved", `{}`, "contacts.getSaved", object{"_": "fixture.vector", "items": []object{{"_": "savedPhoneContact", "phone": "999000000000", "first_name": "Synthetic", "last_name": "Fixture", "date": 100}}}, "Synthetic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				if method != tt.method {
					t.Errorf("method %s", method)
				}
				if method == "users.getFullUser" && asObject(p["id"]).num("access_hash") != 888 {
					t.Error("user reference not account-owned")
				}
				if method == "contacts.getTopPeers" && p["groups"] != true {
					t.Error("wrong category")
				}
				return tt.response, 200
			})
			c.session.Token = "synthetic"
			c.session.UserID = "42"
			c.session.Peers = map[string]object{"user:43": {"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(888)}}
			out, e := c.Call(context.Background(), tt.name, json.RawMessage(tt.body))
			if e != nil || calls != 1 || !strings.Contains(string(out), tt.want) {
				t.Fatalf("result=%s error=%v calls=%d", out, e, calls)
			}
			if strings.Contains(string(out), "access_hash") || strings.Contains(string(out), "notify_settings") {
				t.Errorf("private field leaked: %s", out)
			}
		})
	}
}
