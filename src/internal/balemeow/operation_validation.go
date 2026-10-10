package balemeow

import (
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
)

func prepareExtendedCall(operation string, raw json.RawMessage) (json.RawMessage, error) {
	def, ok := domains.OperationDefinition(operation)
	if !ok {
		return nil, domains.Unsupported(operation)
	}
	if def.Mode != "mutation" {
		v, _, e := domains.NormalizeOperation(operation, raw)
		return v, e
	}
	var values map[string]json.RawMessage
	if len(raw) > 128<<10 || domains.ValidateJSONObject(raw) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid or duplicate mutation fields", 400)
	}
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, boundedError("INVALID_REQUEST", "invalid mutation body", 400)
	}
	var rid string
	if json.Unmarshal(values["request_id"], &rid) != nil {
		return nil, boundedError("INVALID_REQUEST_ID", "a persisted request_id is required", 400)
	}
	if _, err := positiveID(rid); err != nil {
		return nil, boundedError("INVALID_REQUEST_ID", "a persisted positive int64 request_id is required", 400)
	}
	delete(values, "request_id")
	body, _ := json.Marshal(values)
	normalized, _, err := domains.NormalizeOperation(operation, body)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(normalized, &values)
	values["request_id"], _ = json.Marshal(rid)
	return json.Marshal(values)
}
