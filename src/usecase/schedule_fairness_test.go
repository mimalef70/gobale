package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestScheduleCursorProgressesPastFullConnectionAndWraps(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{QueueLimit: 1000, ConnectionQueueLimit: 1}, func(domains.Device) domains.Client { return &fakeClient{} })
	blocked, healthy, newcomer := mustDevice(t, s, "blocked"), mustDevice(t, s, "healthy"), mustDevice(t, s, "newcomer")
	_, err := s.Send(ctx, blocked.ID, sendReq("occupies connection slot"), "full")
	require.NoError(t, err)
	createDue := func(d domains.Device, due time.Time) domains.Schedule {
		r := sendReq("synthetic scheduled occurrence")
		r.Kind, r.Timezone, r.Recurrence, r.ScheduledAt = "text", "UTC", "once", due.Format(time.RFC3339)
		job, err := st.CreateSchedule(ctx, d.ConnectionID, r, due)
		require.NoError(t, err)
		return job
	}
	now := time.Now().UTC().Truncate(time.Second)
	due := now.Add(-time.Hour)
	var blockedJobs []domains.Schedule
	// Equal timestamps exercise the ID tie-breaker at the batch boundary.
	for range 100 {
		blockedJobs = append(blockedJobs, createDue(blocked, due))
	}
	job := createDue(healthy, due.Add(time.Minute))
	cursor, err := s.materializeDueSchedules(now, scheduleCursor{})
	require.NoError(t, err)
	require.NotEmpty(t, cursor.id)
	unprocessed, err := st.GetSchedule(ctx, healthy.ConnectionID, job.ID)
	require.NoError(t, err)
	require.Zero(t, unprocessed.Count, "one pass must attempt at most 100 rows")
	// This row arrives behind the cursor and must be reached after wrapping.
	earlier := createDue(newcomer, due.Add(-time.Minute))
	cursor, err = s.materializeDueSchedules(now, cursor)
	require.NoError(t, err)
	processed, err := st.GetSchedule(ctx, healthy.ConnectionID, job.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, processed.Count)
	require.Equal(t, "completed", processed.State)
	for range 4 {
		cursor, err = s.materializeDueSchedules(now, cursor)
		require.NoError(t, err)
	}
	for _, expected := range []domains.Schedule{job, earlier} {
		occurrences, err := st.ListScheduleOccurrences(ctx, expected.ConnectionID, expected.ID, "", 10, 0)
		require.NoError(t, err)
		require.Len(t, occurrences, 1, "wrap must neither lose nor duplicate occurrences")
	}
	for _, expected := range blockedJobs {
		unchanged, err := st.GetSchedule(ctx, blocked.ConnectionID, expected.ID)
		require.NoError(t, err)
		require.Equal(t, due, unchanged.NextAt)
		require.Zero(t, unchanged.Count)
		require.Equal(t, "active", unchanged.State)
	}
	// Cancellation stops before another batch can materialize any work.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	s.ctx = cancelled
	_, err = s.materializeDueSchedules(now, cursor)
	require.ErrorIs(t, err, context.Canceled)
}

func TestFullConnectionStillProcessesExpiredScheduleBookkeeping(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{ConnectionQueueLimit: 1}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "expired")
	_, err := s.Send(ctx, d.ID, sendReq("occupies slot"), "full")
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	due := now.Add(-2 * time.Hour)
	r := sendReq("expired schedule")
	r.Kind, r.Timezone, r.Recurrence = "text", "UTC", "daily"
	r.ScheduledAt, r.EndAt = due.Format(time.RFC3339), due.Add(time.Hour).Format(time.RFC3339)
	job, err := st.CreateSchedule(ctx, d.ConnectionID, r, due)
	require.NoError(t, err)
	_, err = s.materializeDueSchedules(now, scheduleCursor{})
	require.NoError(t, err)
	got, err := st.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.State)
	require.Zero(t, got.Count)
}
