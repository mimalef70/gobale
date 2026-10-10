package domains

import "encoding/json"

// eventRecord is also the historical storage layout. Do not remove its decoder:
// accepted events and signed webhook bodies survive contract upgrades.
type eventRecord Event

// MarshalJSON puts the consumer-ready message in payload, following the common
// gateway envelope. content retains only the existing reviewed native projection.
// Other event families retain their own payload, including native receipts.
func (e Event) MarshalJSON() ([]byte, error) {
	if e.MessagePatch != nil {
		return json.Marshal(struct {
			eventRecord
			MessagePatch *MessagePatch   `json:"message_patch,omitempty"`
			Payload      *MessagePatch   `json:"payload"`
			Content      json.RawMessage `json:"content"`
		}{eventRecord: eventRecord(e), Payload: e.MessagePatch, Content: e.Payload})
	}
	if e.Message == nil {
		return json.Marshal(eventRecord(e))
	}
	return json.Marshal(struct {
		eventRecord
		Message *Message        `json:"message,omitempty"`
		Payload *Message        `json:"payload"`
		Content json.RawMessage `json:"content"`
	}{eventRecord: eventRecord(e), Payload: e.Message, Content: e.Payload})
}

func (e *Event) UnmarshalJSON(data []byte) error {
	var record struct {
		eventRecord
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	if len(record.Content) > 0 {
		var projection struct {
			Partial bool `json:"partial"`
		}
		if err := json.Unmarshal(record.Payload, &projection); err != nil {
			return err
		}
		if projection.Partial {
			var patch MessagePatch
			if err := json.Unmarshal(record.Payload, &patch); err != nil {
				return err
			}
			record.MessagePatch = &patch
		} else {
			var message Message
			if err := json.Unmarshal(record.Payload, &message); err != nil {
				return err
			}
			record.Message = &message
		}
		record.Payload = record.Content
	}
	*e = Event(record.eventRecord)
	return nil
}
