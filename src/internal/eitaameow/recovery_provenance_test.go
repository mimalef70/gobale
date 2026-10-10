package eitaameow

import (
	"context"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestChannelInvalidationDoesNotInventAccountGap(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		return object{"_": "updates.difference", "new_messages": []object{}, "new_encrypted_messages": []object{}, "chats": []object{}, "users": []object{}, "other_updates": []object{
			{"_": "updateChannelTooLong", "channel_id": int64(91), "pts": 8},
			{"_": "updateChannel", "channel_id": int64(91)},
			{"_": "updateUserStatus", "user_id": int64(43), "status": object{"_": "userStatusEmpty"}},
		}, "state": object{"_": "updates.state", "pts": 9, "qts": 0, "date": 101, "seq": 0, "unread_count": 0}}, 200
	})
	c.session.UserID, c.session.Token = "42", "synthetic"
	cp := checkpoint{Version: 2, Account: "42", PTS: 7, Date: 100}
	raw := checkpointJSON(cp)
	failure := errors.New("commit failed")
	var first domains.EventBatch
	for i := 0; i < 2; i++ {
		next, err := c.pollPage(context.Background(), raw, cp, func(_ context.Context, b domains.EventBatch) error {
			if len(b.Events) != 3 || b.Events[0].Type != "chat.updated" || b.Events[1].Type != "chat.updated" || b.Events[2].Type != "protocol.unsupported_update" {
				t.Fatal("wrong diagnostic projection")
			}
			if i == 0 {
				first = b
				return failure
			}
			if first.Events[0].ID != b.Events[0].ID || first.Events[2].ID != b.Events[2].ID {
				t.Fatal("retry identity changed")
			}
			return nil
		})
		if i == 0 {
			if !errors.Is(err, failure) || next.PTS != 7 || c.Status().UnsupportedUpdatesObserved {
				t.Fatal("failed acceptance changed progress or coverage")
			}
			continue
		}
		if err != nil || next.Gap || !next.Unsupported || next.PTS != 9 {
			t.Fatalf("false recovery gap: %+v %v", next, err)
		}
		c.setRecoveryStatus(next)
		if c.Status().Recovery == "gap_detected" || !c.Status().UnsupportedUpdatesObserved {
			t.Fatal("coverage conflated with loss")
		}
	}
}

func TestEitaaLegacyGapAndExpiredStateHaveDifferentProvenance(t *testing.T) {
	cp, err := parseCheckpoint(`{"version":1,"account":"42","pts":7,"qts":0,"date":100,"seq":0,"gap":true}`, "42")
	if err != nil || cp.Version != 2 || !cp.Gap || cp.GapReason != "legacy_unclassified" {
		t.Fatal("legacy evidence lost")
	}
	c, _ := nativeFixture(t, func(string, object) (object, int) { return object{"_": "updates.differenceTooLong", "pts": 20}, 200 })
	c.session.UserID, c.session.Token = "42", "synthetic"
	c.setRecoveryStatus(cp)
	if c.Status().Recovery == "gap_detected" || c.Status().RecoveryIssue != "legacy_unclassified" {
		t.Fatal("old mixed warning asserted as proven loss")
	}
	next, err := c.pollPage(context.Background(), checkpointJSON(cp), cp, func(context.Context, domains.EventBatch) error { return nil })
	if err != nil || next.GapReason != "difference_too_long" || next.PTS != 20 {
		t.Fatal("expired-state evidence missing")
	}
	c.setRecoveryStatus(next)
	c.setStatus("", "disconnected", "degraded", "CONNECTION_LOST")
	if c.Status().Recovery != "gap_detected" {
		t.Fatal("transport failure erased known gap")
	}
	for _, raw := range []string{
		`{"version":2,"account":"42","pts":7,"date":100,"gap":true}`,
		`{"version":2,"account":"42","pts":7,"date":100,"gap_reason":"difference_too_long"}`,
		`{"version":2,"account":"42","pts":7,"date":100,"gap":true,"gap_reason":"invented"}`,
	} {
		if _, err := parseCheckpoint(raw, "42"); err == nil {
			t.Fatal("invalid provenance accepted")
		}
	}
}

func TestChannelNoticeCannotRebindStreamOrExposePrivateMetadata(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { return nil, 500 })
	for _, u := range []object{{"_": "updateChannelTooLong", "channel_id": int64(92)}, {"_": "updateChannelTooLong", "channel_id": int64(91), "pts": -1}} {
		if _, err := c.projectChannelNotice(u, "91", "cursor"); err == nil {
			t.Fatal("invalid stream accepted")
		}
	}
}

func TestDifferenceSliceMetadataCannotInventProviderProgress(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		return object{"_": "updates.differenceSlice", "new_messages": []object{}, "new_encrypted_messages": []object{}, "other_updates": []object{}, "chats": []object{}, "users": []object{}, "intermediate_state": object{"_": "updates.state", "pts": 7, "qts": 0, "date": 100, "seq": 0, "unread_count": 0}}, 200
	})
	c.session.UserID, c.session.Token = "42", "synthetic"
	cp := checkpoint{Version: 1, Account: "42", PTS: 7, Date: 100}
	if _, err := c.pollPage(context.Background(), checkpointJSON(cp), cp, func(context.Context, domains.EventBatch) error {
		t.Fatal("metadata-only slice progress accepted")
		return nil
	}); err == nil {
		t.Fatal("non-advancing slice accepted")
	}
}
