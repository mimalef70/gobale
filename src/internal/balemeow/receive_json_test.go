package balemeow

import (
	"encoding/json"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"strings"
	"testing"
)

func TestReceiveJSONWhitelist(t *testing.T) {
	tests := []struct{ raw, kind string }{
		{`{"dataType":"location","data":{"location":{"latitude":0,"longitude":0}}}`, "location"},
		{`{"dataType":"location","data":{"location":{"latitude":91,"longitude":0}}}`, "unsupported"},
		{`{"dataType":"location","data":{"location":{"longitude":0}}}`, "unsupported"},
		{`{"dataType":"contact","data":{"contact":{"name":"test","phones":["+989000000000"],"emails":[],"photo":"https://secret.example/private","token":"never"}}}`, "contact"},
		{`{"dataType":"unknown","token":"never"}`, "unsupported"},
		{`{"dataType":"location","dataType":"contact"}`, "unsupported"},
		{`{"dataType":"location","data":{"location":{"latitude":1e1000,"longitude":0}}}`, "unsupported"},
	}
	for _, v := range tests {
		p := messagePayload(&wire.Message{Json: &wire.JSONMessage{RawJson: v.raw}})
		var body map[string]any
		if json.Unmarshal(p, &body) != nil || body["kind"] != v.kind {
			t.Fatalf("%s: %s", v.kind, p)
		}
		if strings.Contains(string(p), "never") || strings.Contains(string(p), "secret.example") {
			t.Fatal("private unreviewed field leaked")
		}
	}
}
