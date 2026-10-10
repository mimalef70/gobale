package eitaameow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func migrationNotice() object {
	return object{"_": "messageService", "id": 0, "peer_id": object{"_": "peerChannel", "channel_id": 91}, "date": 100, "action": object{"_": "messageActionChannelMigrateFrom", "chat_id": 50, "title": "private title"}}
}

func TestZeroIDMigrationIsConversationEvent(t *testing.T) {
	c, _ := New(Config{})
	first, err := c.projectMessage(migrationNotice(), "message")
	second, err2 := c.projectMessage(migrationNotice(), "message")
	if err != nil || err2 != nil || first.Type != "chat.migrated" || first.MessageID != "" || first.Message != nil || first.ID != second.ID || strings.Contains(string(first.Payload), "private") {
		t.Fatalf("invalid migration projection %#v %v", first, err)
	}
	for _, mutate := range []func(object){
		func(m object) { delete(m, "id") },
		func(m object) { m["peer_id"] = object{"_": "peerUser", "user_id": 91} },
		func(m object) { m["date"] = 0 },
		func(m object) { asObject(m["action"])["chat_id"] = 0 },
		func(m object) { asObject(m["action"])["_"] = "unknownAction" },
	} {
		m := migrationNotice()
		mutate(m)
		if _, err := c.projectMessage(m, "message"); err == nil {
			t.Fatal("malformed zero-ID notice accepted")
		}
	}
}

func TestMigrationNoticeCannotBlockOrPartiallyCommitDifference(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, _ object) (object, int) {
		if method != "updates.getDifference" {
			t.Fatal("wrong method")
		}
		return object{"_": "updates.difference", "new_messages": []object{{"_": "message", "id": 12, "peer_id": object{"_": "peerUser", "user_id": 99}, "date": 100, "message": "synthetic"}}, "other_updates": []object{{"_": "updateNewChannelMessage", "message": migrationNotice(), "pts": 8, "pts_count": 1}}, "new_encrypted_messages": []object{}, "chats": []object{}, "users": []object{}, "state": object{"_": "updates.state", "pts": 11, "qts": 0, "date": 100, "seq": 0, "unread_count": 0}}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	cp := checkpoint{Version: 1, Account: "42", PTS: 10, Date: 99}
	before := checkpointJSON(cp)
	failure := errors.New("commit failed")
	ids := []string{}
	for _, fail := range []bool{true, false} {
		next, err := c.pollPage(context.Background(), before, cp, func(_ context.Context, b domains.EventBatch) error {
			if len(b.Events) != 2 || len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != before {
				t.Fatal("page not accepted atomically")
			}
			for i, e := range b.Events {
				if fail {
					ids = append(ids, e.ID)
				} else if e.ID != ids[i] {
					t.Fatal("retry identity changed")
				}
			}
			if b.Events[1].Type != "chat.migrated" || b.Events[1].MessageID != "" || b.Events[1].MediaRevision != nil {
				t.Fatal("fake message identity")
			}
			var stored checkpoint
			_ = json.Unmarshal([]byte(b.Checkpoints[0].Next), &stored)
			if stored.PTS != 11 || stored.Gap {
				t.Fatal("wrong next checkpoint")
			}
			if fail {
				return failure
			}
			return nil
		})
		if fail {
			if !errors.Is(err, failure) || next != cp {
				t.Fatal("failed commit advanced checkpoint")
			}
		} else if err != nil || next.PTS != 11 {
			t.Fatalf("migration blocked page: %v", err)
		}
	}
}
