package eitaameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewedAdditionalReadWireAndPrivacy(t *testing.T) {
	tests := []struct {
		name, body, method, want string
		response                 object
	}{
		{"users.common_chats", `{"user":"43","limit":1}`, "messages.getCommonChats", `"next_cursor"`, object{"_": "messages.chats", "chats": []object{{"_": "chatForbidden", "id": int64(99), "title": "Synthetic"}}}},
		{"poll.voters", `{"peer":{"type":"user","id":"43"},"message_id":"12","option":"AA==","limit":1}`, "messages.getPollVotes", `"9007199254740993"`, object{"_": "messages.votesList", "count": 2, "votes": []object{{"_": "messageUserVote", "user_id": int64(9007199254740993), "option": []byte{0}, "date": 100}}, "users": []object{}, "next_offset": "private-provider-offset"}},
		{"message.read_participants", `{"peer":{"type":"user","id":"43"},"message_id":"12"}`, "messages.getMessageReadParticipants", `"9007199254740993"`, object{"_": "fixture.vector", "items": []int64{9007199254740993}}},
		{"message.views", `{"peer":{"type":"user","id":"43"},"message_id":"12"}`, "messages.getMessagesViews", `"views":123`, object{"_": "messages.messageViews", "views": []object{{"_": "messageViews", "views": 123, "forwards": 2}}, "users": []object{}, "chats": []object{}}},
		{"messages.search", `{"query":"Synthetic","limit":1}`, "messages.searchGlobalExt", `"id":"12"`, object{"_": "messages.messages", "messages": []object{{"_": "message", "id": 12, "peer_id": object{"_": "peerUser", "user_id": int64(43)}, "date": 100, "message": "Synthetic"}}, "users": []object{}, "chats": []object{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				if method != tt.method {
					t.Errorf("method=%s", method)
				}
				if method == "messages.getMessagesViews" && p["increment"] != false {
					t.Error("read must not increment views")
				}
				if method == "messages.getPollVotes" && string(p["option"].([]byte)) != "\x00" {
					t.Error("option bytes changed")
				}
				return tt.response, 200
			})
			c.session.Token = "synthetic"
			c.session.UserID = "42"
			c.session.Peers = map[string]object{"user:43": {"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(888)}}
			out, e := c.Call(context.Background(), tt.name, json.RawMessage(tt.body))
			if e != nil || calls != 1 || !strings.Contains(string(out), tt.want) {
				t.Fatalf("out=%s err=%v calls=%d", out, e, calls)
			}
			if strings.Contains(string(out), "access_hash") || strings.Contains(string(out), "private-provider-offset") {
				t.Fatalf("private reference leaked: %s", out)
			}
		})
	}
}
func TestAdditionalReadCursorScopeAndMalformedResponses(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		return object{"_": "messages.messageViews", "views": []object{}, "users": []object{}, "chats": []object{}}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	c.session.Peers = map[string]object{"user:43": {"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(888)}}
	cursor := c.encodeValueCursor("poll.voters:peer:12", "provider-offset")
	if _, e := c.decodeValueCursor(cursor, "poll.voters:peer:13"); e == nil {
		t.Fatal("cursor rebound to message")
	}
	c.cfg.ConnectionID = "another-connection"
	if _, e := c.decodeValueCursor(cursor, "poll.voters:peer:12"); e == nil {
		t.Fatal("cursor rebound to connection")
	}
	if _, e := c.Call(context.Background(), "message.views", json.RawMessage(`{"peer":{"type":"user","id":"43"},"message_id":"12"}`)); e == nil {
		t.Fatal("malformed views accepted")
	}
	if _, e := c.Call(context.Background(), "poll.voters", json.RawMessage(`{"peer":{"type":"user","id":"43"},"message_id":"12","option":"%%%"}`)); e == nil {
		t.Fatal("invalid option accepted")
	}
}
func TestGlobalSearchCursorBindsQueryAndDoesNotLoop(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls++
		if method != "messages.searchGlobalExt" {
			t.Errorf("method=%s", method)
		}
		return object{"_": "messages.messagesSlice", "count": 2, "messages": []object{{"_": "message", "id": 12, "peer_id": object{"_": "peerUser", "user_id": int64(43)}, "date": 100, "message": "Synthetic"}}, "users": []object{}, "chats": []object{}}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	c.session.Peers = map[string]object{"user:43": {"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(888)}}
	out, e := c.Call(context.Background(), "messages.search", json.RawMessage(`{"query":"Synthetic","limit":1}`))
	if e != nil {
		t.Fatal(e)
	}
	var page map[string]any
	_ = json.Unmarshal(out, &page)
	cursor, _ := page["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("no cursor: %s", out)
	}
	body, _ := json.Marshal(map[string]any{"query": "Changed", "limit": 1, "cursor": cursor})
	if _, e = c.Call(context.Background(), "messages.search", body); e == nil || calls != 1 {
		t.Fatal("query cursor did not reject before RPC")
	}
	body, _ = json.Marshal(map[string]any{"query": "Synthetic", "limit": 1, "cursor": cursor})
	out, e = c.Call(context.Background(), "messages.search", body)
	if e != nil || !strings.Contains(string(out), "non_advancing_cursor") {
		t.Fatalf("out=%s err=%v", out, e)
	}
}
