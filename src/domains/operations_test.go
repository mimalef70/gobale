package domains

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtendedAdmissionRejectsPrivateFieldsAndAmbiguousJSON(t *testing.T) {
	for _, body := range []string{
		`{"peer":{"type":"group","id":"77","access_hash":"999"},"user":{"type":"user","id":"42"}}`,
		`{"peer":{"type":"group","id":"77"},"user":{"type":"user","id":"42"},"request_id":"9"}`,
		`{"peer":{"type":"group","id":"77"},"peer":{"type":"group","id":"78"},"user":{"type":"user","id":"42"}}`,
		`{"peer":{"type":"group","id":"4294967296"},"user":{"type":"user","id":"42"}}`,
		`{"peer":{"type":"group","id":77},"user":{"type":"user","id":"42"}}`,
		`null`, `[]`, `{} {}`,
	} {
		if _, _, err := NormalizeOperation("group.promote", json.RawMessage(body)); err == nil {
			t.Errorf("accepted invalid input %s", body)
		}
	}
}

func TestExtendedAdmissionPreservesInt64AndLiteralText(t *testing.T) {
	input := json.RawMessage(`{"peer":{"type":"channel","id":"77"},"message_id":"-9223372036854775808","date":"1720000000000","just_mine":true}`)
	output, p, err := NormalizeOperation("message.pin", input)
	if err != nil || p.Type != "channel" || !strings.Contains(string(output), `"-9223372036854775808"`) {
		t.Fatal(string(output), p, err)
	}
	for _, bad := range []string{"0", "9223372036854775808", "-9223372036854775809"} {
		body := strings.Replace(string(input), "-9223372036854775808", bad, 1)
		if _, _, err := NormalizeOperation("message.pin", json.RawMessage(body)); err == nil {
			t.Error("unbounded RID accepted", bad)
		}
	}
	for _, text := range []string{"الف\u200cب 💙", `الف\u200cب`, "line\nnext"} {
		body, _ := json.Marshal(map[string]string{"name": text})
		out, _, err := NormalizeOperation("account.name", body)
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]string
		_ = json.Unmarshal(out, &values)
		if values["name"] != text {
			t.Fatal("text changed")
		}
	}
}

func TestOperationCatalogHasUniqueRoutesAndBoundedSchemas(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range OperationDefinitions() {
		key := entry.Method + " " + entry.Path
		if seen[key] {
			t.Errorf("duplicate route %s", key)
		}
		seen[key] = true
		if entry.Mode == "mutation" && entry.Method != "POST" {
			t.Fatal(entry.Operation)
		}
		if entry.Request.AdditionalProperties {
			t.Fatal("unbounded root object", entry.Operation)
		}
		if _, ok := entry.Request.Properties["request_id"]; ok {
			t.Fatal("public caller may override request ID", entry.Operation)
		}
	}
}

func TestStoryReportAdmissionRejectsAmbiguousIDs(t *testing.T) {
	for _, body := range []string{
		`{"story_ids":["same","same"],"kind":5}`,
		`{"story_ids":["\n"],"kind":5}`,
		`{"story_ids":["   "],"kind":5}`,
		`{"story_ids":[],"kind":5}`,
		`{"story_ids":["good"],"kind":0}`,
		`{"story_ids":["good"],"kind":5,"peer":{"type":"user","id":"42"}}`,
	} {
		if _, _, err := NormalizeOperation("report.story", json.RawMessage(body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	if _, _, err := NormalizeOperation("report.story", json.RawMessage(`{"story_ids":["opaque:story-42"],"kind":5}`)); err != nil {
		t.Fatal(err)
	}
}
