package usecase

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestScheduledForwardReferencesAndFreshOccurrenceIDs(t *testing.T) {
	ctx := context.Background()
	calls := []map[string]any{}
	fake := &fakeClient{status: readyStatus(), callFn: func(_ context.Context, operation string, raw json.RawMessage) (json.RawMessage, error) {
		require.Equal(t, "message.forward", operation)
		var p map[string]any
		require.NoError(t, json.Unmarshal(raw, &p))
		calls = append(calls, p)
		return nil, &domains.Error{Code: "SEND_UNKNOWN", Message: "synthetic lost acknowledgement", HTTP: 202, Ambiguous: true}
	}}
	svc, st := testService(t, Options{}, func(domains.Device) domains.Client { return fake })
	d := mustDevice(t, svc, "forward")
	request := domains.SendRequest{Operation: "message.forward", Payload: json.RawMessage(`{"peer":{"type":"user","id":"42"},"source_peer":{"type":"group","id":"73"},"message_id":"-123","source_date":"1791300000000","hide_sender":false}`)}
	request.ScheduledAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	request.Timezone = "UTC"
	request.Recurrence = "daily"
	job, err := svc.CreateScheduleIdempotent(ctx, d.ID, request, "schedule-forward")
	require.NoError(t, err)
	require.Zero(t, fake.callCalls, "admission must not consult provider or require local source history")
	count, err := st.EventCount(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Zero(t, count)
	second := job.NextAt.Add(24 * time.Hour)
	first, err := st.MaterializeSchedule(ctx, d.ConnectionID, job.ID, job.NextAt, &second, storage.AdmissionLimits{Global: 10})
	require.NoError(t, err)
	secondOp, err := st.MaterializeSchedule(ctx, d.ConnectionID, job.ID, second, nil, storage.AdmissionLimits{Global: 10})
	require.NoError(t, err)
	require.NotEqual(t, first.Request.RequestID, secondOp.Request.RequestID)
	listed, err := svc.ListOperationsFiltered(ctx, d.ID, domains.OperationFilter{Operation: "message.forward", ScheduleID: job.ID})
	require.NoError(t, err)
	require.Len(t, listed, 2)
	for _, op := range []domains.Operation{first, secondOp} {
		svc.processOperation(claimOne(t, st))
		got, err := st.GetOperation(ctx, d.ConnectionID, op.ID)
		require.NoError(t, err)
		require.Equal(t, "unknown", got.State)
	}
	require.Len(t, calls, 2)
	require.Equal(t, first.Request.RequestID, calls[0]["request_id"])
	require.Equal(t, secondOp.Request.RequestID, calls[1]["request_id"])
	require.Equal(t, "-123", calls[0]["message_id"])
	require.Equal(t, "1791300000000", calls[0]["source_date"])
	pending, err := st.ClaimOperations(ctx, 5)
	require.NoError(t, err)
	require.Empty(t, pending, "unknown forward must not be resent")
	repeated, err := svc.CreateScheduleIdempotent(ctx, d.ID, request, "schedule-forward")
	require.NoError(t, err)
	require.Equal(t, job.ID, repeated.ID)
	bad := request
	bad.Operation = "message.delete"
	_, err = svc.CreateSchedule(ctx, d.ID, bad)
	codeIs(t, err, "INVALID_REQUEST")
	bad = request
	bad.Text = "conflict"
	_, err = svc.CreateSchedule(ctx, d.ID, bad)
	codeIs(t, err, "INVALID_REQUEST")
	bad = request
	bad.Payload = json.RawMessage(`{"peer":{"type":"user","id":"42"},"source_peer":{"type":"group","id":"73"},"message_id":"-123","source_date":"0"}`)
	_, err = svc.CreateSchedule(ctx, d.ID, bad)
	codeIs(t, err, "INVALID_REQUEST")
}
