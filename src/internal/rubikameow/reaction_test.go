package rubikameow

import (
	"context"
	"encoding/json"
	"testing"
)

func TestReactionRemovalOmitsIDAndAddPreservesString(t *testing.T) {
	for _, remove := range []bool{false, true} {
		name := "message.reaction"
		raw := json.RawMessage(`{"peer":{"type":"user","id":"u0peer"},"message_id":"42","reaction_id":"1"}`)
		if remove {
			name += ".remove"
			raw = json.RawMessage(`{"peer":{"type":"user","id":"u0peer"},"message_id":"42"}`)
		}
		normalized, _, err := (Contract{}).NormalizeOperation(name, raw)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
			if method != "actionOnMessageReaction" || input.str("message_id") != "42" || input.str("object_guid") != "u0peer" {
				t.Fatal("reaction target changed")
			}
			if remove {
				if input["reaction_id"] != nil || input.str("action") != "Remove" {
					t.Fatal("Remove must omit reaction_id")
				}
			} else if id, ok := input["reaction_id"].(string); !ok || id != "1" || input.str("action") != "Add" {
				t.Fatal("Add must preserve string ID")
			}
			return object{"status": "OK"}
		})
		var body object
		_ = json.Unmarshal(normalized, &body)
		body["request_id"] = "77"
		normalized, _ = json.Marshal(body)
		if _, err = c.Call(context.Background(), name, normalized); err != nil {
			t.Fatal(err)
		}
	}
}
