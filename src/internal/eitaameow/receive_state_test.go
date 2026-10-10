package eitaameow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestStateUpdateProjectionPreservesProvenanceWithoutInventingPeerOrTime(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("state projection contacted provider"); return nil, 500 })
	c.session.Peers = map[string]object{"group:channel_91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(888)}}
	cases := []struct {
		name, kind string
		value      object
		count      int
		gap        bool
	}{
		{"channel deletion", "message.deleted", object{"_": "updateDeleteChannelMessages", "channel_id": int64(91), "messages": []any{int32(9), int32(7), int32(9)}, "pts": 8, "pts_count": 2}, 2, false},
		{"unresolved deletion", "message.deleted.unresolved", object{"_": "updateDeleteMessages", "messages": []any{int32(7)}, "pts": 8, "pts_count": 1}, 1, true},
		{"own read", "message.read_by_me", object{"_": "updateReadHistoryInbox", "peer": object{"_": "peerUser", "user_id": int64(43)}, "max_id": 12, "still_unread_count": 0, "pts": 8, "pts_count": 1}, 1, false},
		{"peer read", "message.read", object{"_": "updateReadHistoryOutbox", "peer": object{"_": "peerUser", "user_id": int64(43)}, "max_id": 12, "pts": 8, "pts_count": 1}, 1, false},
		{"channel own read", "message.read_by_me", object{"_": "updateReadChannelInbox", "channel_id": int64(91), "max_id": 12, "still_unread_count": 2, "pts": 8}, 1, false},
		{"channel peer read without pts", "message.read", object{"_": "updateReadChannelOutbox", "channel_id": int64(91), "max_id": 12}, 1, false},
		{"channel content read without pts", "message.content_read", object{"_": "updateChannelReadMessagesContents", "channel_id": int64(91), "messages": []any{int32(7), int32(8)}}, 1, false},
		{"unresolved content read", "message.content_read.unresolved", object{"_": "updateReadMessagesContents", "messages": []any{int32(7)}, "pts": 8, "pts_count": 1}, 1, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			events, handled, gap, e := c.projectStateUpdate(tt.value, "")
			if e != nil || !handled || gap != tt.gap || len(events) != tt.count {
				t.Fatalf("events=%#v handled=%v gap=%v error=%v", events, handled, gap, e)
			}
			repeat, _, _, e := c.projectStateUpdate(tt.value, "")
			if e != nil {
				t.Fatal(e)
			}
			for i, event := range events {
				if event.Type != tt.kind || event.ID != repeat[i].ID || event.Direction != "unknown" || event.Time.IsZero() {
					t.Fatalf("bad projection %#v", event)
				}
				var payload map[string]any
				_ = json.Unmarshal(event.Payload, &payload)
				if payload["timestamp_source"] != "observed" {
					t.Fatal("unlabeled observation time")
				}
				for _, key := range []string{"read_date", "received_date", "access_hash", "start_date"} {
					if _, present := payload[key]; present {
						t.Fatalf("invented/leaked field %s", key)
					}
				}
				if tt.gap && event.Peer.ID != "" {
					t.Fatal("guessed peer")
				}
				if tt.kind == "message.deleted" {
					if event.MediaRevision == nil || event.MediaRevision.Scope != "channel:91" || event.MediaRevision.Sequence != 8 {
						t.Fatalf("missing deletion revision: %#v", event.MediaRevision)
					}
				} else if event.MediaRevision != nil {
					t.Fatalf("invented non-media revision: %#v", event.MediaRevision)
				}
				if tt.value.num("channel_id") == 91 && event.Peer.Type != "group" {
					t.Fatal("lost resolved supergroup subtype")
				}
				if _, hasPTS := tt.value["pts"]; !hasPTS {
					if _, invented := payload["pts"]; invented {
						t.Fatal("invented page PTS")
					}
				}
			}
		})
	}
}
func TestStateUpdateRejectsBoundsAndChannelRebinding(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { return nil, 500 })
	cases := []object{
		{"_": "updateReadHistoryOutbox", "peer": object{"_": "peerChat", "chat_id": int64(91)}, "max_id": 12, "pts": 8, "pts_count": 1},
		{"_": "updateDeleteChannelMessages", "channel_id": int64(92), "messages": []any{int32(1)}, "pts": 8, "pts_count": 1},
		{"_": "updateDeleteChannelMessages", "channel_id": int64(91), "messages": []any{int32(-1)}, "pts": 8, "pts_count": 1},
		{"_": "updateReadChannelOutbox", "channel_id": int64(91), "max_id": int64(2147483648)},
		{"_": "updateReadChannelInbox", "channel_id": int64(91), "max_id": 1, "still_unread_count": -1, "pts": 8},
		{"_": "updateDeleteMessages", "messages": []any{int32(1)}, "pts": 8, "pts_count": 1},
	}
	for _, value := range cases {
		if _, handled, _, e := c.projectStateUpdate(value, "91"); e == nil || !handled {
			t.Fatalf("invalid scoped update accepted %#v", value)
		}
	}
	huge := make([]any, 1001)
	for i := range huge {
		huge[i] = int32(i + 1)
	}
	if _, _, _, e := c.projectStateUpdate(object{"_": "updateDeleteMessages", "messages": huge, "pts": 8, "pts_count": 1}, ""); e == nil {
		t.Fatal("unbounded message IDs accepted")
	}
}
func TestStateUpdateBatchAndCheckpointRemainAtomicOnFailure(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method != "updates.getDifference" {
			t.Errorf("method=%s", method)
		}
		return object{"_": "updates.difference", "new_messages": []object{}, "new_encrypted_messages": []object{}, "other_updates": []object{{"_": "updateReadHistoryInbox", "peer": object{"_": "peerUser", "user_id": int64(43)}, "max_id": 12, "still_unread_count": 0, "pts": 8, "pts_count": 1}, {"_": "updateDeleteMessages", "messages": []int64{7}, "pts": 9, "pts_count": 1}}, "chats": []object{}, "users": []object{}, "state": object{"_": "updates.state", "pts": 9, "qts": 0, "date": 101, "seq": 0, "unread_count": 0}}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	cp := checkpoint{Version: 1, Account: "42", PTS: 7, Date: 100}
	expected := checkpointJSON(cp)
	failure := errors.New("synthetic commit failure")
	ids := []string{}
	for range 2 {
		next, e := c.pollPage(context.Background(), expected, cp, func(_ context.Context, b domains.EventBatch) error {
			if len(b.Events) != 2 || len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != expected || !strings.Contains(b.Checkpoints[0].Next, `"gap":true`) {
				t.Fatalf("non-atomic page %#v", b)
			}
			ids = append(ids, b.Events[0].ID, b.Events[1].ID)
			return failure
		})
		if !errors.Is(e, failure) || next.PTS != 7 {
			t.Fatalf("failed acceptance advanced %+v %v", next, e)
		}
	}
	if ids[0] != ids[2] || ids[1] != ids[3] {
		t.Fatal("retry changed event identity")
	}
}
func TestChannelStateUpdatesUseSelectedRecoveryScope(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		return object{"_": "updates.channelDifference", "pts": 9, "final": true, "new_messages": []object{}, "other_updates": []object{{"_": "updateDeleteChannelMessages", "channel_id": int64(91), "messages": []int64{7}, "pts": 8, "pts_count": 1}, {"_": "updateReadChannelOutbox", "channel_id": int64(91), "max_id": 12}}, "chats": []object{}, "users": []object{}}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	raw := channelCheckpointJSON(channelCheckpoint{Version: 1, Account: "42", Channel: "91", PTS: 7})
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return raw, nil }
	calls := 0
	e := c.pollChannelPage(context.Background(), "91", object{"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(2)}, func(_ context.Context, b domains.EventBatch) error {
		calls++
		if len(b.Events) != 2 || b.Events[0].Type != "message.deleted" || b.Events[1].Type != "message.read" || strings.Contains(b.Checkpoints[0].Next, `"gap":true`) {
			t.Fatalf("wrong state page %#v", b)
		}
		return nil
	})
	if e != nil || calls != 1 {
		t.Fatalf("error=%v calls=%d", e, calls)
	}
}

func TestStateUpdateExpansionCannotExceedBatchBound(t *testing.T) {
	ids := make([]int64, 600)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		return object{"_": "updates.difference", "new_messages": []object{}, "new_encrypted_messages": []object{}, "other_updates": []object{{"_": "updateDeleteChannelMessages", "channel_id": int64(91), "messages": ids, "pts": 8, "pts_count": 1}, {"_": "updateDeleteChannelMessages", "channel_id": int64(92), "messages": ids, "pts": 9, "pts_count": 1}}, "chats": []object{}, "users": []object{}, "state": object{"_": "updates.state", "pts": 9, "qts": 0, "date": 101, "seq": 0, "unread_count": 0}}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	cp := checkpoint{Version: 1, Account: "42", PTS: 7, Date: 100}
	next, e := c.pollPage(context.Background(), checkpointJSON(cp), cp, func(context.Context, domains.EventBatch) error {
		t.Fatal("oversized expanded page accepted")
		return nil
	})
	if e == nil || next.PTS != 7 {
		t.Fatalf("oversized page advanced: %+v %v", next, e)
	}
}
