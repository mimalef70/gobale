package rubikameow

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestDiscoveredConversationAcceptsMessagesAndBaselineAtomically(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		if method != "getMessagesInterval" || input.str("object_guid") != "u0peer" || input.str("middle_message_id") != "42" {
			t.Fatal("wrong baseline request")
		}
		return object{"state": "mutation101", "messages": []any{object{"message_id": "42", "time": 1700000000, "type": "Text", "text": "synthetic", "author_object_guid": "u0peer"}}}
	})
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return "", nil }
	failure := errors.New("commit failed")
	var first domains.EventBatch
	for i := 0; i < 2; i++ {
		_, err := c.recoverPeer(context.Background(), pendingChat{GUID: "u0peer", LastMessage: "42"}, func(_ context.Context, b domains.EventBatch) error {
			if len(b.Events) != 2 || b.Events[0].MessageID != "42" || b.Events[1].Type != "connection.recovery" || len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != "" {
				t.Fatal("messages skipped or accepted separately")
			}
			cp, e := parseCheckpoint(b.Checkpoints[0].Next, "u0self")
			if e != nil || cp.LastMessage != "42" || cp.State != "mutation101" || cp.Peer != "u0peer" {
				t.Fatal("wrong baseline")
			}
			if i == 0 {
				first = b
				return failure
			}
			if first.Events[0].ID != b.Events[0].ID || first.Events[1].ID != b.Events[1].ID {
				t.Fatal("retry identity changed")
			}
			return nil
		})
		if (i == 0 && !errors.Is(err, failure)) || (i == 1 && err != nil) {
			t.Fatal(err)
		}
	}
}

func TestBusyRecoveryPeerRotatesBehindOtherConversations(t *testing.T) {
	visited := []string{}
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		visited = append(visited, input.str("object_guid"))
		if method == "getMessagesUpdates" {
			return object{"new_state": "101", "updated_messages": []any{}}
		}
		if method == "getMessages" {
			return object{"messages": []any{object{"message_id": "42", "time": 1700000000, "type": "Text", "text": "synthetic"}}, "has_continue": true}
		}
		t.Fatal("wrong method")
		return nil
	})
	c.cfg.LoadCheckpoint = func(_ context.Context, scope string) (string, error) {
		guid := scope[len("rubika.messages:"):]
		last := "45"
		if guid == "u0busy" {
			last = "41"
		}
		return checkpointJSON(checkpoint{Version: 2, Account: "u0self", Peer: guid, State: "100", LastMessage: last}), nil
	}
	cp := checkpoint{Version: 2, Account: "u0self", State: "100", Pending: []pendingChat{{GUID: "u0busy", LastMessage: "45"}, {GUID: "u0quiet", LastMessage: "45"}, {GUID: "u0quiet2", LastMessage: "45"}, {GUID: "u0quiet3", LastMessage: "45"}, {GUID: "u0later", LastMessage: "45"}}}
	next, err := c.recoverPage(context.Background(), checkpointJSON(cp), cp, func(context.Context, domains.EventBatch) error { return nil })
	if err != nil || len(next.Pending) != 2 || next.Pending[0].GUID != "u0later" || next.Pending[1].GUID != "u0busy" || len(visited) != 5 {
		t.Fatalf("head peer starved neighbours: %+v %v", next, err)
	}
}

func TestInitialMessageWindowRejectsForeignPeerBeforeCommit(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object {
		return object{"state": "101", "messages": []any{object{"object_guid": "u0foreign", "message_id": "42", "time": 1700000000, "type": "Text"}}}
	})
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return "", nil }
	if _, err := c.recoverPeer(context.Background(), pendingChat{GUID: "u0peer", LastMessage: "42"}, func(context.Context, domains.EventBatch) error { t.Fatal("foreign message committed"); return nil }); err == nil {
		t.Fatal("foreign baseline accepted")
	}
}

func TestRetainedGapDoesNotDisableLaterNewMessageRecovery(t *testing.T) {
	methods := []string{}
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		methods = append(methods, method)
		if method == "getMessagesUpdates" {
			return object{"new_state": "101", "updated_messages": []any{}}
		}
		return object{"messages": []any{object{"message_id": "42", "time": 1700000000, "type": "Text", "text": "synthetic"}}, "has_continue": false}
	})
	cp := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100", LastMessage: "41", Gap: true, GapReason: "message_state_expired"}
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return checkpointJSON(cp), nil }
	accepted := 0
	gap, err := c.recoverPeer(context.Background(), pendingChat{GUID: "u0peer", LastMessage: "42"}, func(_ context.Context, b domains.EventBatch) error {
		accepted += len(b.Events)
		next, e := parseCheckpoint(b.Checkpoints[0].Next, cp.Account)
		if e != nil || !next.Gap || next.GapReason != cp.GapReason {
			t.Fatal("old loss evidence cleared")
		}
		return nil
	})
	if err != nil || !gap || accepted != 1 || len(methods) != 2 || methods[1] != "getMessages" {
		t.Fatal("historical gap stalled current messages")
	}
}

func TestExpiredMutationBaselinePreservesNewMessageBackfillAcrossRestart(t *testing.T) {
	cp := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", LastMessage: "41", Gap: true, GapReason: "message_state_expired"}
	raw := checkpointJSON(cp)
	requested := []string{}
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		requested = append(requested, method+":"+input.str("min_id"))
		message := func(n int) object {
			return object{"message_id": strconv.Itoa(n), "time": 1700000000 + n, "type": "Text", "text": "synthetic", "author_object_guid": "u0peer"}
		}
		switch method {
		case "getMessagesInterval":
			return object{"state": "mutation200", "messages": []any{message(70), message(69)}}
		case "getMessagesUpdates":
			return object{"new_state": "mutation201", "updated_messages": []any{}}
		case "getMessages":
			if input.str("min_id") == "42" {
				return object{"messages": []any{message(42), message(43)}, "has_continue": true}
			}
			if input.str("min_id") != "44" {
				t.Fatal("latest interval skipped the missing older pages")
			}
			rows := []any{}
			for n := 44; n <= 70; n++ {
				rows = append(rows, message(n))
			}
			return object{"messages": rows, "has_continue": false}
		}
		t.Fatal("unexpected method")
		return nil
	})
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return raw, nil }
	ids := map[string]string{}
	commits := 0
	sink := func(_ context.Context, batch domains.EventBatch) error {
		transition := batch.Checkpoints[0]
		if transition.Expected != raw {
			t.Fatal("backfill did not resume its last durable checkpoint")
		}
		next, err := parseCheckpoint(transition.Next, cp.Account)
		if err != nil || !next.Gap || next.GapReason != "message_state_expired" {
			t.Fatal("backfill falsely repaired missing edit/delete history")
		}
		if commits == 0 && next.LastMessage != "41" {
			t.Fatal("mutation rebaseline overwrote the new-message watermark")
		}
		for _, event := range batch.Events {
			if event.Type != "message" {
				continue
			}
			if previous := ids[event.MessageID]; previous != "" && previous != event.ID {
				t.Fatal("overlapping interval/backfill changed event identity")
			}
			ids[event.MessageID] = event.ID
		}
		raw = transition.Next
		commits++
		return nil
	}
	peer := pendingChat{GUID: "u0peer", LastMessage: "70"}
	gap, err := c.recoverPeer(context.Background(), peer, sink)
	if !gap || !errors.Is(err, errMoreMessages) {
		t.Fatal("bounded backfill did not retain pending progress")
	}
	first, err := parseCheckpoint(raw, cp.Account)
	if err != nil || first.LastMessage != "43" || first.State != "mutation200" {
		t.Fatal("wrong durable restart boundary")
	}
	gap, err = c.recoverPeer(context.Background(), peer, sink)
	last, parseErr := parseCheckpoint(raw, cp.Account)
	if err != nil || parseErr != nil || !gap || last.LastMessage != "70" || len(ids) != 29 || len(requested) != 4 {
		t.Fatalf("incomplete resumed backfill: messages=%d requests=%v error=%v", len(ids), requested, err)
	}
}
