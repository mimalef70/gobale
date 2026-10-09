package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func TestSendHTTPProjectionKeepsAcknowledgementDistinctFromPending(t *testing.T) {
	for _, state := range []string{"queued", "sending", "unknown", "failed", "cancelled", "succeeded"} {
		op := domains.Operation{ID: "synthetic-operation", State: state, Request: domains.SendRequest{Kind: "text"}, Result: &domains.SendResult{MessageID: "-9007199254740993"}}
		body, err := json.Marshal(operationResponse(op))
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(body, &fields))
		require.Equal(t, state, fields["state"])
		require.Equal(t, op.ID, fields["send_id"])
		require.NotContains(t, fields, "result")
		if state == "succeeded" {
			require.Equal(t, "-9007199254740993", fields["message_id"])
		} else {
			require.NotContains(t, fields, "message_id", "an unacknowledged operation cannot look sent")
		}
	}
}

func TestMediaJSONContractUsesCaptionAndRejectsUnsupportedOptions(t *testing.T) {
	s, svc := setupAPI(t, "")
	s.opts.SendWait = 10 * time.Millisecond
	d, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	require.NoError(t, s.store.SaveMedia(context.Background(), domains.Media{ID: "media-one", ConnectionID: d.ConnectionID, Name: "one.ogg", Path: "synthetic", ContentType: "audio/ogg", Size: 12}))
	status, body := apiRequest(t, s, "POST", "/send/audio", "one", map[string]any{"phone": "+10000000000", "media_id": "media-one", "caption": "متن 🙂", "ptt": true})
	require.Equal(t, 202, status, body)
	op := body["results"].(map[string]any)
	require.Equal(t, "queued", op["state"])
	require.NotContains(t, op, "message_id")
	request := op["request"].(map[string]any)
	require.Equal(t, "voice", request["kind"])
	require.Equal(t, "متن 🙂", request["message"])
	for _, field := range []string{"message", "request_id", "view_once", "compress", "audio_url"} {
		status, body := apiRequest(t, s, "POST", "/send/audio", "one", map[string]any{"phone": "+10000000000", "media_id": "media-one", field: "unsupported"})
		require.Equal(t, 400, status, body)
	}
	status, body = apiRequest(t, s, "POST", "/send/schedules", "one", map[string]any{"kind": "text", "operation": "send.poll", "payload": map[string]any{}, "scheduled_at": "2030-01-01T00:00:00Z", "timezone": "UTC"})
	require.Equal(t, 400, status, body)
}

func TestAccountNameHTTPContractPreservesStoredArguments(t *testing.T) {
	s, svc := setupAPI(t, "")
	s.opts.SendWait = 10 * time.Millisecond
	d, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	for _, path := range []string{"/user/name", "/operations/account.name"} {
		r := httptest.NewRequest("POST", path, bytes.NewBufferString(`{"push_name":"نام آزمایشی"}`))
		r.SetBasicAuth("test", "password")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", path)
		r.Header.Set("X-Device-Id", "one")
		scopeTestRequest(t, s, r)
		res, err := s.App.Test(r)
		require.NoError(t, err)
		require.Equal(t, 202, res.StatusCode)
		res.Body.Close()
		status, _ := apiRequest(t, s, "POST", path, "one", map[string]any{"name": "old field"})
		require.Equal(t, 400, status)
	}
	ops, err := s.store.ListOperations(context.Background(), d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Len(t, ops, 2)
	for _, op := range ops {
		require.JSONEq(t, `{"name":"نام آزمایشی"}`, string(op.Request.Payload))
	}
	status, body := apiRequest(t, s, "GET", "/app/capabilities", "", nil)
	require.Equal(t, 200, status)
	for _, raw := range body["results"].([]any) {
		entry := raw.(map[string]any)
		if entry["operation"] == "account.name" {
			props := entry["request"].(map[string]any)["properties"].(map[string]any)
			require.Contains(t, props, "push_name")
			require.NotContains(t, props, "name")
		}
	}
}

func TestMultipartScheduleRetainsOriginalFileOnRetryAndConflict(t *testing.T) {
	s, svc := setupAPI(t, "")
	d, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	metadata, err := json.Marshal(map[string]any{"phone": "+10000000000", "caption": "later", "scheduled_at": at, "timezone": "UTC"})
	require.NoError(t, err)
	var id string
	for _, content := range []string{"synthetic bytes", "synthetic bytes", "changed bytes"} {
		res, err := s.App.Test(multipartRequest(t, s, "one", "/send/audio", "scheduled-upload", string(metadata), content, "ptt", "true"))
		require.NoError(t, err)
		if content == "changed bytes" {
			require.Equal(t, 409, res.StatusCode)
			res.Body.Close()
			continue
		}
		require.Equal(t, 200, res.StatusCode)
		var response struct {
			Results scheduledSend `json:"results"`
		}
		require.NoError(t, json.NewDecoder(res.Body).Decode(&response))
		res.Body.Close()
		require.Equal(t, "Message scheduled", response.Results.Status)
		require.Equal(t, at, response.Results.ScheduledAt.Format(time.RFC3339))
		if id != "" {
			require.Equal(t, id, response.Results.ScheduleID)
		}
		id = response.Results.ScheduleID
	}
	jobs, err := s.store.ListSchedules(context.Background(), d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "voice", jobs[0].Request.Kind)
	ops, err := s.store.ListOperations(context.Background(), d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Empty(t, ops, "the upload cannot send before its occurrence")
	assertUploadFiles(t, s, 1)
}
