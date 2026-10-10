package rubikameow

import (
	"context"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestNewMessageWatermarkIndependentOfMutationState(t *testing.T) {
	requests := 0
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		requests++
		if method != "getMessages" || input.str("sort") != "FromMin" || input.str("min_id") != "42" || input.str("object_guid") != "u0peer" {
			t.Fatal("incorrect bounded new-message request")
		}
		return object{"messages": []any{object{"message_id": "41", "time": 1699999999, "type": "Text", "text": "already accepted", "author_object_guid": "u0peer"}, object{"message_id": "42", "time": 1700000000, "type": "Text", "text": "synthetic", "author_object_guid": "u0peer"}}, "has_continue": true}
	})
	cp := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "mutation101", LastMessage: "41"}
	p := pendingChat{GUID: "u0peer", LastMessage: "45"}
	var accepted domains.EventBatch
	err := c.recoverNewMessages(context.Background(), p, cp, func(_ context.Context, b domains.EventBatch) error { accepted = b; return nil })
	if !errors.Is(err, errMoreMessages) || len(accepted.Events) != 1 || accepted.Events[0].Direction != "incoming" || len(accepted.Checkpoints) != 1 || accepted.Checkpoints[0].Expected != checkpointJSON(cp) {
		t.Fatal("new message and progress were not durably accepted together")
	}
	next, err := parseCheckpoint(accepted.Checkpoints[0].Next, cp.Account)
	if err != nil || next.LastMessage != "42" || next.State != cp.State {
		t.Fatal("mutation state overwritten or unseen messages skipped")
	}
	failure := errors.New("commit failed")
	if err = c.recoverNewMessages(context.Background(), p, cp, func(context.Context, domains.EventBatch) error { return failure }); !errors.Is(err, failure) || requests != 2 {
		t.Fatal("persistence failure became success or caused a hidden retry")
	}
}

func TestNewMessageRecoveryRejectsNonAdvancingPage(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object {
		return object{"messages": []any{}, "has_continue": true}
	})
	cp := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100", LastMessage: "41"}
	if err := c.recoverNewMessages(context.Background(), pendingChat{GUID: "u0peer", LastMessage: "45"}, cp, func(context.Context, domains.EventBatch) error { t.Fatal("invalid page committed"); return nil }); err == nil {
		t.Fatal("non-advancing page accepted")
	}
}

func TestNewMessageRecoveryRejectsForeignPeer(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object {
		return object{"messages": []any{object{"object_guid": "u0other", "message_id": "42", "time": 1700000000, "type": "Text", "text": "synthetic"}}, "has_continue": false}
	})
	cp := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100", LastMessage: "41"}
	if err := c.recoverNewMessages(context.Background(), pendingChat{GUID: "u0peer", LastMessage: "45"}, cp, func(context.Context, domains.EventBatch) error { t.Fatal("foreign message committed"); return nil }); err == nil {
		t.Fatal("foreign peer accepted")
	}
}

func TestLegacyMessageCursorBaselineIsExplicitAndScoped(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		if method != "getMessagesInterval" || input.str("middle_message_id") != "42" {
			t.Fatal("legacy cursor inferred from local time")
		}
		return object{"messages": []any{object{"message_id": "42", "time": 1700000000, "type": "Text", "text": "synthetic", "author_object_guid": "u0peer"}}}
	})
	cp := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100"}
	if err := c.recoverNewMessages(context.Background(), pendingChat{GUID: "u0peer", LastMessage: "42"}, cp, func(_ context.Context, b domains.EventBatch) error {
		if len(b.Events) != 2 || b.Events[1].Type != "connection.recovery" || b.Checkpoints[0].Scope != "rubika.messages:u0peer" {
			t.Fatal("legacy bounded import not marked or scoped")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
