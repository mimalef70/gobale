package rest

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/stretchr/testify/require"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoreQueryRESTContractsAndConnectionScope(t *testing.T) {
	var calls atomic.Int32
	s, svc := setupAPIWithFactory(t, "", func(domains.Device) domains.Client { calls.Add(1); return &testClient{} })
	ctx := context.Background()
	d, err := svc.CreateDevice(ctx, "one")
	require.NoError(t, err)
	_, err = svc.CreateDevice(ctx, "two")
	require.NoError(t, err)
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	job, err := s.store.CreateSchedule(ctx, d.ConnectionID, domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Kind: "text", Text: "fixture"}, due)
	require.NoError(t, err)
	op, err := s.store.MaterializeSchedule(ctx, d.ConnectionID, job.ID, due, nil, storage.AdmissionLimits{Global: 20})
	require.NoError(t, err)
	status, body := apiRequest(t, s, "GET", "/send/operations?schedule_id="+job.ID+"&kind=text&peer=user:42", "one", nil)
	require.Equal(t, 200, status)
	rows := body["results"].([]any)
	require.Len(t, rows, 1)
	require.Equal(t, op.ID, rows[0].(map[string]any)["send_id"])
	status, body = apiRequest(t, s, "GET", "/send/schedules/"+job.ID+"/occurrences", "one", nil)
	require.Equal(t, 200, status)
	require.Len(t, body["results"].([]any), 1)
	status, body = apiRequest(t, s, "GET", "/send/schedules?state=completed", "one", nil)
	require.Equal(t, 200, status)
	rows = body["results"].([]any)
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0].(map[string]any)["occurrence_history_complete"])
	status, _ = apiRequest(t, s, "GET", "/send/schedules/"+job.ID+"/occurrences", "two", nil)
	require.Equal(t, 404, status)
	ev := domains.Event{ID: "synthetic", Type: "message", Peer: domains.Peer{Type: "user", ID: "42"}, SenderID: "7", Direction: "incoming", Time: time.Now().UTC(), Payload: json.RawMessage(`{"kind":"text","message":"سلام %_🙂"}`)}
	_, err = s.store.AppendEvent(ctx, d.ConnectionID, ev, nil)
	require.NoError(t, err)
	for _, path := range []string{"/events?peer=user:42&search=", "/chat/user:42/messages?search="} {
		status, body = apiRequest(t, s, "GET", path+url.QueryEscape("سلام %_🙂"), "one", nil)
		require.Equal(t, 200, status)
		require.Len(t, body["results"].([]any), 1)
	}
	for _, path := range []string{"/events?media_only=bad", "/events?direction=bad", "/events?sender_id=0", "/events?start_time=bad", "/events?peer=user:01", "/send/operations?state=bad", "/send/schedules?state=bad"} {
		status, _ = apiRequest(t, s, "GET", path, "one", nil)
		require.Equal(t, 400, status, path)
	}
	require.LessOrEqual(t, calls.Load(), int32(1), "query routes must not instantiate provider clients (worker may claim the synthetic operation)")
	require.NoError(t, svc.DeleteDevice(ctx, "one"))
	_, err = svc.CreateDevice(ctx, "one")
	require.NoError(t, err)
	status, _ = apiRequest(t, s, "GET", "/send/schedules/"+job.ID+"/occurrences", "one", nil)
	require.Equal(t, 404, status)
	status, body = apiRequest(t, s, "GET", "/events", "one", nil)
	require.Equal(t, 200, status)
	require.Empty(t, body["results"].([]any))
}
func TestWebhookFilterPatchOmissionClearAndInvalid(t *testing.T) {
	s, _ := setupAPI(t, "")
	status, body := apiRequest(t, s, "POST", "/devices", "", map[string]any{"device_id": "filter", "webhook_filter": map[string]any{"peer_types": []string{"group"}, "directions": []string{"incoming"}}})
	require.Equal(t, 201, status, body)
	status, body = apiRequest(t, s, "PATCH", "/devices/filter/webhook", "", map[string]any{})
	require.Equal(t, 200, status)
	filter := body["results"].(map[string]any)["webhook_filter"].(map[string]any)
	require.Equal(t, []any{"group"}, filter["peer_types"])
	status, body = apiRequest(t, s, "GET", "/devices/filter/webhook", "", nil)
	require.Equal(t, 200, status)
	require.NotEmpty(t, body["results"].(map[string]any)["webhook_filter"])
	status, body = apiRequest(t, s, "PATCH", "/devices/filter/webhook", "", map[string]any{"webhook_filter": map[string]any{}})
	require.Equal(t, 200, status)
	require.Empty(t, body["results"].(map[string]any)["webhook_filter"])
	for _, f := range []any{nil, map[string]any{"sender_ids": []string{"0"}}, map[string]any{"sender_ids": []string{"7", "7"}}, map[string]any{"unknown": true}} {
		status, _ = apiRequest(t, s, "PATCH", "/devices/filter/webhook", "", map[string]any{"webhook_filter": f})
		require.Equal(t, 400, status)
	}
}
