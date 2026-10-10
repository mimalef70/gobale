package rubikameow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestSparseEditRecoveryCommitsPatchBeforeProgress(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		if method != "getMessagesUpdates" || input.str("state") != "100" {
			t.Fatalf("unexpected recovery request %s", method)
		}
		return object{"status": "OK", "new_state": "101", "updated_messages": []any{object{
			"object_guid": "u0peer", "action": "Edit", "message_id": "42", "timestamp": "revision2",
			"updated_parameters": []any{"text", "metadata", "is_edited"},
			"message":            object{"text": "synthetic edited text", "is_edited": true, "metadata": object{"access_hash": "secret"}},
		}}}
	})
	cp := checkpointJSON(checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100"})
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return cp, nil }
	accept := func(_ context.Context, batch domains.EventBatch) error {
		if len(batch.Events) != 1 || len(batch.Checkpoints) != 1 || batch.Checkpoints[0].Expected != cp {
			t.Fatal("patch and progress must share a batch")
		}
		event := batch.Events[0]
		if event.Type != "message.edited" || event.MessageID != "42" || event.Message != nil || event.SenderID != "" || event.Direction != "unknown" {
			t.Fatal("partial edit invented full message provenance")
		}
		var p object
		if json.Unmarshal(event.Payload, &p) != nil || p["partial"] != true || p.str("text") != "synthetic edited text" || p["unprojected_fields"] != true || strings.Contains(string(event.Payload), "secret") {
			t.Fatal("unsafe or missing patch projection")
		}
		return nil
	}
	if _, err := c.recoverPeer(context.Background(), pendingChat{GUID: "u0peer"}, accept); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("storage failure")
	if _, err := c.recoverPeer(context.Background(), pendingChat{GUID: "u0peer"}, func(ctx context.Context, batch domains.EventBatch) error {
		_ = accept(ctx, batch)
		return failure
	}); !errors.Is(err, failure) {
		t.Fatal("storage rejection lost")
	}
	if c.cfg.LoadCheckpoint == nil || !strings.Contains(cp, `"state":"100"`) {
		t.Fatal("failed acceptance changed durable checkpoint")
	}
}
