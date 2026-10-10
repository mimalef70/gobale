package eitaameow

import (
	"context"
	"encoding/json"
	"testing"
)

func TestObservedGroupAdminMethodsUseScopedPeers(t *testing.T) {
	tests := []struct{ name, method, extra string }{
		{"group.description", "messages.editChatAbout", `,"description":"synthetic"`},
		{"group.signatures", "channels.toggleSignatures", `,"enabled":false`},
		{"group.protect_content", "messages.toggleNoForwards", `,"enabled":true`},
		{"group.username.available", "channels.checkUsername", `,"username":"synthetic"`},
		{"group.online", "messages.getOnlines", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				if method != tt.method {
					t.Error(method)
				}
				ref := asObject(p["peer"])
				if ref == nil {
					ref = asObject(p["channel"])
				}
				if ref.num("access_hash") != 777 {
					t.Error("did not use account scoped reference")
				}
				if method == "channels.toggleSignatures" && p["enabled"] != false {
					t.Error("false toggle lost")
				}
				if tt.name == "group.online" {
					return object{"_": "chatOnlines", "onlines": 7}, 200
				}
				if tt.name == "group.description" {
					return object{"_": "boolTrue"}, 200
				}
				if tt.name == "group.username.available" {
					return object{"_": "boolFalse"}, 200
				}
				return emptyUpdates(), 200
			})
			mediaClientSource(c, nil)
			c.session.Peers = map[string]object{"channel:91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(777)}}
			out, err := c.Call(context.Background(), tt.name, json.RawMessage(`{"peer":{"type":"channel","id":"91"},"request_id":"900"`+tt.extra+`}`))
			if err != nil || calls != 1 {
				t.Fatalf("operation %s %v", out, err)
			}
			if tt.name == "group.username.available" && string(out) != `{"available":false}` {
				t.Fatalf("false availability lost: %s", out)
			}
		})
	}
}
func TestGroupAdminRejectsUserAndMissingToggle(t *testing.T) {
	for _, raw := range []string{`{"peer":{"type":"user","id":"42"},"enabled":true}`, `{"peer":{"type":"channel","id":"91"}}`} {
		if _, _, err := (Contract{}).NormalizeOperation("group.signatures", json.RawMessage(raw)); err == nil {
			t.Fatal("invalid group administration accepted")
		}
	}
}
