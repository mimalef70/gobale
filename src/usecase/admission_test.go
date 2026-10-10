package usecase

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/domains/send"
	"github.com/stretchr/testify/require"
)

func TestConfiguredConnectionAdmissionAppliesToSendsMutationsAndSchedule(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{QueueLimit: 10, ConnectionQueueLimit: 1}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "limited")
	other := mustDevice(t, s, "independent")
	first, err := s.Send(ctx, d.ID, sendReq("synthetic"), "once")
	require.NoError(t, err)
	_, err = s.Send(ctx, d.ID, sendReq("overflow"), "overflow")
	codeIs(t, err, "CONNECTION_QUEUE_FULL")
	again, err := s.Send(ctx, d.ID, sendReq("synthetic"), "once")
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID)
	_, err = s.Mutate(ctx, d.ID, "message.forward", json.RawMessage(`{"source_peer":{"type":"user","id":"12"},"peer":{"type":"user","id":"13"},"message_id":"42","source_date":"1791300000000"}`), "forward")
	codeIs(t, err, "CONNECTION_QUEUE_FULL")
	_, err = s.Send(ctx, other.ID, sendReq("independent"), "once")
	require.NoError(t, err)
	due := time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)
	request := sendReq("scheduled")
	request.ScheduleOptions = send.ScheduleOptions{ScheduledAt: due.Format(time.RFC3339), Timezone: "UTC"}
	job, err := s.CreateScheduleIdempotent(ctx, d.ID, request, "schedule")
	require.NoError(t, err)
	s.materializeSchedule(job)
	unchanged, err := st.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	require.Zero(t, unchanged.Count)
	require.Equal(t, job.NextAt, unchanged.NextAt)
	require.Equal(t, "active", unchanged.State)
}
