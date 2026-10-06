package storage

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScheduleIdempotencyPersistenceAndTerminalReplay(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "schedule")
	req := textRequest("scheduled message")
	next := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	req.ScheduledAt = next.Format(time.RFC3339)
	first, err := s.CreateScheduleIdempotent(ctx, d.ConnectionID, req, next, "create-once")
	require.NoError(t, err)
	duplicate, err := s.CreateScheduleIdempotent(ctx, d.ConnectionID, req, next.Add(time.Hour), "create-once")
	require.NoError(t, err)
	require.Equal(t, first, duplicate)
	require.NoError(t, s.SetScheduleState(ctx, d.ConnectionID, first.ID, "completed"))
	require.NoError(t, s.Close())
	reopened, err := Open(path, testKey)
	require.NoError(t, err)
	defer reopened.Close()
	got, found, err := reopened.LookupScheduleIdempotent(ctx, d.ConnectionID, req, "create-once")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first.ID, got.ID)
	require.Equal(t, "completed", got.State)
	got, err = reopened.CreateScheduleIdempotent(ctx, d.ConnectionID, req, time.Time{}, "create-once")
	require.NoError(t, err)
	require.Equal(t, first.ID, got.ID)
	req.Text = "different"
	_, _, err = reopened.LookupScheduleIdempotent(ctx, d.ConnectionID, req, "create-once")
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, err = reopened.CreateScheduleIdempotent(ctx, d.ConnectionID, req, next, "create-once")
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
}

func TestScheduleIdempotencyCrossTypeAndConnectionIsolation(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	a, b := device(t, s, "a"), device(t, s, "b")
	req := textRequest("hello")
	next := time.Now().UTC().Add(time.Hour)
	first, err := s.CreateScheduleIdempotent(ctx, a.ConnectionID, req, next, "shared")
	require.NoError(t, err)
	_, _, err = s.Enqueue(ctx, a.ConnectionID, req, "shared", 10)
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, _, err = s.Enqueue(ctx, b.ConnectionID, req, "shared", 10)
	require.NoError(t, err)
	_, err = s.CreateScheduleIdempotent(ctx, b.ConnectionID, req, next, "shared")
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, _, err = s.LookupScheduleIdempotent(ctx, b.ConnectionID, req, "shared")
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	require.NoError(t, s.DeleteDevice(ctx, a.ConnectionID))
	a = device(t, s, "a")
	replacement, err := s.CreateScheduleIdempotent(ctx, a.ConnectionID, req, next, "shared")
	require.NoError(t, err)
	require.NotEqual(t, first.ID, replacement.ID)
	_, err = s.CreateScheduleIdempotent(ctx, a.ConnectionID, req, next, strings.Repeat("x", 257))
	errorCode(t, err, "INVALID_IDEMPOTENCY_KEY")
}

func TestScheduleIdempotencyConcurrentCreationAndEmptyKey(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "a")
	req := textRequest("hello")
	next := time.Now().UTC().Add(time.Hour)
	const concurrency = 12
	errors := make(chan error, concurrency)
	ids := make(chan string, concurrency)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Go(func() {
			job, err := s.CreateScheduleIdempotent(ctx, d.ConnectionID, req, next, "once")
			errors <- err
			ids <- job.ID
		})
	}
	wg.Wait()
	close(errors)
	close(ids)
	for err := range errors {
		require.NoError(t, err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		require.Equal(t, first, id)
	}
	_, err := s.CreateSchedule(ctx, d.ConnectionID, req, next)
	require.NoError(t, err)
	_, err = s.CreateSchedule(ctx, d.ConnectionID, req, next)
	require.NoError(t, err)
	jobs, err := s.ListSchedules(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 3)
}

func TestScheduleIdempotencyMigrationFromV2PreservesExistingSchedule(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "a")
	req := textRequest("hello")
	next := time.Now().UTC().Add(time.Hour)
	old, err := s.CreateSchedule(ctx, d.ConnectionID, req, next)
	require.NoError(t, err)
	_, err = s.db.Exec(`DROP TABLE schedule_idempotency`)
	require.NoError(t, err)
	_, err = s.db.Exec(`DROP INDEX deliveries_pending_order`)
	require.NoError(t, err)
	_, err = s.db.Exec(`DROP INDEX deliveries_inflight_target`)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE gobale_meta SET version=2`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	upgraded, err := Open(path, testKey)
	require.NoError(t, err)
	defer upgraded.Close()
	got, err := upgraded.GetSchedule(ctx, d.ConnectionID, old.ID)
	require.NoError(t, err)
	require.Equal(t, old, got)
	_, err = upgraded.CreateScheduleIdempotent(ctx, d.ConnectionID, req, next, "new")
	require.NoError(t, err)
}
