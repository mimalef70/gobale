package rubikameow

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
)

func TestHistoryLimitCannotSkipProviderRows(t *testing.T) {
	requests := 0
	c, _ := newRPCFixture(t, func(method string, p object, _ bool) object {
		requests++
		if method != "getMessages" || p.num("limit") != 2 || p.str("sort") != "FromMax" {
			t.Fatal("unbounded history request")
		}
		maxID := int64(6)
		if p.str("max_id") != "" {
			maxID, _ = strconv.ParseInt(p.str("max_id"), 10, 64)
		}
		rows := []any{}
		// Model a provider which ignores the requested limit and has no more
		// pages of its own. Locally omitted rows still need a safe next boundary.
		for id := maxID; id > 0; id-- {
			rows = append(rows, object{"message_id": strconv.FormatInt(id, 10), "time": 1700000000, "type": "Text", "text": "synthetic"})
		}
		return object{"messages": rows, "has_continue": false, "new_max_id": "1"}
	})
	offset := ""
	ids := []string{}
	for range 3 {
		input := object{"peer": peerFromGUID("u0peer"), "limit": 2}
		if offset != "" {
			input["offset_id"] = offset
		}
		body, _ := json.Marshal(input)
		raw, err := c.Call(context.Background(), "chat.history", body)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Next string `json:"next_offset_id"`
			More bool   `json:"has_continue"`
		}
		if json.Unmarshal(raw, &result) != nil || len(result.Items) != 2 {
			t.Fatal("incorrect bounded page")
		}
		for _, m := range result.Items {
			ids = append(ids, m.ID)
		}
		offset = result.Next
	}
	if requests != 3 || len(ids) != 6 {
		t.Fatal("incorrect traversal")
	}
	for i, id := range ids {
		if id != strconv.Itoa(6-i) {
			t.Fatal("message skipped or duplicated between pages")
		}
	}
}

func TestHistoryRejectsWrongPeerAndMalformedContinuedPage(t *testing.T) {
	for _, o := range []object{
		{"messages": []any{}, "has_continue": true},
		{"messages": []any{object{"object_guid": "u0foreign", "message_id": "42", "time": 1700000000, "type": "Text"}}, "has_continue": false},
		{"messages": []any{object{"message_id": "43", "time": 1700000000, "type": "Text"}}, "has_continue": false},
	} {
		c, _ := newRPCFixture(t, func(string, object, bool) object { return o })
		if _, err := c.Call(context.Background(), "chat.history", json.RawMessage(`{"peer":{"type":"user","id":"u0peer"},"offset_id":"42","limit":2}`)); err == nil {
			t.Fatal("invalid scoped history page accepted")
		}
	}
}
