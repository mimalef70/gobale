package rubikameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestGlobalSearchUsesReviewedResultTimestamp(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		if method != "searchGlobalMessages" {
			t.Fatal("wrong method")
		}
		return object{"messages": []any{object{"object_guid": "u0peer", "time": 1700000000,
			"abs_object": object{"access_hash": "secret"},
			"message":    object{"message_id": "42", "text": "synthetic", "type": "Text", "author_object_guid": "u0peer"}}}, "has_continue": false}
	})
	out, err := c.Call(context.Background(), "search.messages", json.RawMessage(`{"query":"synthetic","search_type":"Text"}`))
	if err != nil || !strings.Contains(string(out), `"timestamp":"2023-11-14T22:13:20Z"`) || strings.Contains(string(out), "secret") {
		t.Fatalf("search timestamp or safe projection failed: %v", err)
	}
}
