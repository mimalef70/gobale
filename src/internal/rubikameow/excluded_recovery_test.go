package rubikameow

import (
	"context"
	"errors"
	"github.com/mimalef70/goomni/src/domains"
	"testing"
)

func TestBotServiceConversationsParticipateInDurableRecovery(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		if method != "getChats" {
			t.Fatalf("unexpected call %s", method)
		}
		return object{"state": "101", "chats": []any{object{"object_guid": "s0service"}, object{"object_guid": "b0bot"}, object{"object_guid": "u0peer", "last_message_id": "42"}}, "has_continue": false}
	})
	cp := checkpoint{Version: 2, Account: "u0self", Listing: true}
	var accepted domains.EventBatch
	next, err := c.recoverPage(context.Background(), "", cp, func(_ context.Context, b domains.EventBatch) error { accepted = b; return nil })
	if err != nil || next.State != "101" || next.Gap || next.Listing || len(next.Pending) != 3 || next.Pending[0].GUID != "s0service" {
		t.Fatalf("progress: %+v %v", next, err)
	}
	if len(accepted.Events) != 4 || len(accepted.Checkpoints) != 1 {
		t.Fatalf("batch: %+v", accepted)
	}
	for _, e := range accepted.Events[:2] {
		if e.Type != "chat.updated" || (e.Peer.Type != "service" && e.Peer.Type != "bot") {
			t.Fatal("bot/service peer reinterpreted as user")
		}
	}
	failed, err := c.recoverPage(context.Background(), "", cp, func(context.Context, domains.EventBatch) error { return errors.New("storage failed") })
	if err == nil || failed.State != "" || len(failed.Pending) != 0 {
		t.Fatal("failed acceptance advanced checkpoint")
	}
}
