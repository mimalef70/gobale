package balemeow

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestReceiptRangeLiteralsNeverInventMessageProof(t *testing.T) {
	// These bytes are independent of the generated encoder. Update fields 19
	// and 54 contain Peer (1), start_date (2), date (3); field 50 uses a wrapped
	// end_date (4). Small synthetic milliseconds keep their layout inspectable.
	for _, tc := range []struct {
		name, wire, kind, start, end, status string
		valid                                bool
	}{
		{"read ordered", "9a010a0a040801102a107b187c", "message.read", "123", "124", "valid", true},
		{"received ordered", "b2030a0a040801102a107b187c", "message.received", "123", "124", "valid", true},
		{"read zero end", "9a010a0a040801102a107b1800", "message.read", "123", "0", "unknown", false},
		{"received zero end", "b2030a0a040801102a107b1800", "message.received", "123", "0", "unknown", false},
		{"read omitted end", "9a01080a040801102a107b", "message.read", "123", "0", "unknown", false},
		{"zero start", "9a010a0a040801102a1000187c", "message.read", "0", "124", "unknown", false},
		{"reversed", "9a010a0a040801102a107c187b", "message.read", "124", "123", "invalid", false},
		{"equal bounds", "9a010a0a040801102a107b187b", "message.read", "123", "123", "valid", true},
		{"own read missing end", "9203080a040801102a107b", "message.read_by_me", "123", "", "unknown", false},
		{"own read bounded", "92030c0a040801102a107b2202087c", "message.read_by_me", "123", "124", "valid", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := hex.DecodeString(tc.wire)
			if err != nil {
				t.Fatal(err)
			}
			events, err := decodeEvents("99", raw)
			if err != nil || len(events) != 1 || events[0].Type != tc.kind {
				t.Fatalf("events=%+v err=%v", events, err)
			}
			event := events[0]
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			endKey := "date"
			if tc.kind == "message.read_by_me" {
				endKey = "end_date"
			}
			if payload["start_date"] != tc.start || (tc.end != "" && payload[endKey] != tc.end) || (tc.end == "" && payload[endKey] != nil) {
				t.Fatalf("receipt bounds changed: %s", event.Payload)
			}
			if payload["range_status"] != tc.status || payload["range_valid"] != tc.valid || payload["message_ids_supported"] != false {
				t.Fatalf("unexpected range interpretation: %s", event.Payload)
			}
			if event.MessageID != "" || payload["message_ids"] != nil || payload["message_id"] != nil {
				t.Fatalf("receipt invented message proof: %+v", event)
			}
			again, err := decodeEvents("99", raw)
			if err != nil || len(again) != 1 || again[0].ID != event.ID {
				t.Fatal("receipt identity changed on replay", again, err)
			}
		})
	}
}
