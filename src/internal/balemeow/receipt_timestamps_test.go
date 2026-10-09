package balemeow

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestReceiptTimestampLiteralsNeverInventMessageProof(t *testing.T) {
	// These bytes are independent of the generated encoder. Update fields 19
	// and 54 contain Peer (1), startDate (2), readDate/receivedDate (3); field
	// 50 uses a wrapped endDate (4). Web 5.7.0+173855, observed 2026-10-09:
	// /static/js/async/4825.fc21701758.js (codec) and
	// /static/js/async/modulesBuilder.e7abdf3264.js (watermark consumers).
	// Small synthetic milliseconds keep the layout inspectable.
	for _, tc := range []struct {
		name, wire, kind, start, dateKey, date string
	}{
		{"read dates", "9a010a0a040801102a107b187c", "message.read", "123", "read_date", "124"},
		{"received dates", "b2030a0a040801102a107b187c", "message.received", "123", "received_date", "124"},
		{"read zero observation", "9a010a0a040801102a107b1800", "message.read", "123", "read_date", "0"},
		{"received zero observation", "b2030a0a040801102a107b1800", "message.received", "123", "received_date", "0"},
		{"read omitted observation", "9a01080a040801102a107b", "message.read", "123", "read_date", "0"},
		{"received omitted observation", "b203080a040801102a107b", "message.received", "123", "received_date", "0"},
		{"zero start", "9a010a0a040801102a1000187c", "message.read", "0", "read_date", "124"},
		{"read earlier observation", "9a010a0a040801102a107c187b", "message.read", "124", "read_date", "123"},
		{"received earlier observation", "b2030a0a040801102a107c187b", "message.received", "124", "received_date", "123"},
		{"equal timestamps", "9a010a0a040801102a107b187b", "message.read", "123", "read_date", "123"},
		{"timestamps above JavaScript precision", "9a01180a040801102a108180808080808010188280808080808010", "message.read", "9007199254740993", "read_date", "9007199254740994"},
		{"own read missing end", "9203080a040801102a107b", "message.read_by_me", "123", "end_date", ""},
		{"own read dates", "92030c0a040801102a107b2202087c", "message.read_by_me", "123", "end_date", "124"},
		{"own read explicit zero end", "92030a0a040801102a107b2200", "message.read_by_me", "123", "end_date", "0"},
		{"own read earlier end", "92030c0a040801102a107c2202087b", "message.read_by_me", "124", "end_date", "123"},
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
			if payload["start_date"] != tc.start || (tc.date != "" && payload[tc.dateKey] != tc.date) || (tc.date == "" && payload[tc.dateKey] != nil) {
				t.Fatalf("receipt timestamps changed: %s", event.Payload)
			}
			for _, key := range []string{"range_status", "range_valid", "date", "message_ids", "message_id"} {
				if _, ok := payload[key]; ok {
					t.Fatalf("receipt invented a range, alias or message proof: %s", event.Payload)
				}
			}
			if event.MessageID != "" || payload["message_ids_supported"] != false {
				t.Fatalf("receipt invented message proof: %+v", event)
			}
			// IDs predate this projection correction and are based on the wire,
			// not JSON keys: re-observing an old event must still deduplicate it.
			if want := eventHash("99|" + tc.kind + "|user:42|" + string(raw)); event.ID != want {
				t.Fatalf("receipt projection changed source identity: %s != %s", event.ID, want)
			}
			again, err := decodeEvents("99", raw)
			if err != nil || len(again) != 1 || again[0].ID != event.ID {
				t.Fatal("receipt identity changed on replay", again, err)
			}
		})
	}
}

func TestReceiptNegativeTimestampLiteralsRejected(t *testing.T) {
	for _, literal := range []string{
		"9a01130a040801102a10ffffffffffffffffff01187b", // Negative startDate.
		"9a01130a040801102a107b18ffffffffffffffffff01", // Negative readDate.
		"b203130a040801102a107b18ffffffffffffffffff01", // Negative receivedDate.
	} {
		raw, err := hex.DecodeString(literal)
		if err != nil {
			t.Fatal(err)
		}
		if events, err := decodeEvents("99", raw); err == nil || len(events) != 0 {
			t.Fatalf("negative receipt timestamp was accepted: %+v, %v", events, err)
		}
	}
}
