package rest

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
)

func decodeSendRequest(c fiber.Ctx, kind string) (domains.SendRequest, error) {
	var fields map[string]json.RawMessage
	if err := decode(c, &fields); err != nil {
		return domains.SendRequest{}, err
	}
	return sendRequestFields(fields, kind)
}

// Public field names are translated here; persisted requests retain the stable
// canonical representation and content hashes of already accepted work.
func sendRequestFields(fields map[string]json.RawMessage, kind string) (domains.SendRequest, error) {
	invalid := func() (domains.SendRequest, error) {
		return domains.SendRequest{}, domains.E("INVALID_REQUEST", "invalid or unsupported send field", 400)
	}
	scheduleEndpoint := kind == ""
	if scheduleEndpoint {
		kind = "text"
		if raw, ok := fields["kind"]; ok && json.Unmarshal(raw, &kind) != nil {
			return invalid()
		}
		if _, ok := fields["operation"]; ok {
			if _, explicit := fields["kind"]; explicit && kind != "operation" {
				return invalid()
			}
			kind = "operation"
		}
	}
	if kind == "message" {
		kind = "text"
	}
	media := kind == "file" || kind == "image" || kind == "audio" || kind == "video" || kind == "voice"
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return invalid()
		}
		switch name {
		case "peer", "phone", "mentions", "reply_message_id", "scheduled_at", "timezone", "recurrence", "weekdays", "day_of_month", "end_at", "occurrence_limit":
		case "kind", "operation", "payload":
			if !scheduleEndpoint {
				return invalid()
			}
		case "media_id", "caption":
			if !media {
				return invalid()
			}
		case "message":
			if kind != "text" {
				return invalid()
			}
		case "ptt":
			if kind != "audio" {
				return invalid()
			}
		default:
			return invalid()
		}
	}
	if caption, ok := fields["caption"]; ok {
		fields["message"] = caption
		delete(fields, "caption")
	}
	if raw, ok := fields["ptt"]; ok {
		var ptt bool
		if json.Unmarshal(raw, &ptt) != nil {
			return invalid()
		}
		if ptt {
			kind = "voice"
		}
		delete(fields, "ptt")
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return invalid()
	}
	var request domains.SendRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return invalid()
	}
	request.Kind = kind
	return request, nil
}

type scheduledSend struct {
	Status      string    `json:"status"`
	ScheduleID  string    `json:"schedule_id"`
	ScheduledAt time.Time `json:"scheduled_at"`
	NextRunAt   time.Time `json:"next_run_at"`
}

func scheduledSendResponse(job domains.Schedule) scheduledSend {
	// Keyed retries preserve the original requested time after an occurrence.
	at, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(job.Request.ScheduledAt))
	return scheduledSend{Status: "Message scheduled", ScheduleID: job.ID, ScheduledAt: at, NextRunAt: job.NextAt}
}

// Keep the durable operation model internal. HTTP places an acknowledged
// message_id directly in results while retaining the operation's real state.
type operationView struct {
	domains.Operation
	Result *domains.SendResult `json:"result,omitempty"`
	*domains.SendResult
	Status string `json:"status"`
}

func operationResponse(op domains.Operation) operationView {
	v := operationView{Operation: op, Status: op.State}
	if op.State == "succeeded" {
		v.SendResult = op.Result
		v.Status = "Message sent"
		if op.Request.Kind == "operation" {
			v.Status = "Operation acknowledged"
		}
	}
	return v
}

func publicResults(value any) any {
	switch v := value.(type) {
	case domains.Operation:
		return operationResponse(v)
	case []domains.Operation:
		out := make([]operationView, len(v))
		for i := range v {
			out[i] = operationResponse(v[i])
		}
		return out
	case []domains.ScheduleOccurrence:
		type occurrence struct {
			domains.ScheduleOccurrence
			Operation operationView `json:"operation"`
		}
		out := make([]occurrence, len(v))
		for i := range v {
			out[i] = occurrence{v[i], operationResponse(v[i].Operation)}
		}
		return out
	default:
		return value
	}
}
