package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestScheduledUploadAtomicityConcurrencyRestartAndSharedKeys(t *testing.T) {
	s, path := testStore(t)
	ctx := context.Background()
	d := device(t, s, "upload-schedule")
	req := textRequest("caption")
	req.Kind = "file"
	req.ScheduledAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	req.Timezone = "UTC"
	next, err := time.Parse(time.RFC3339, req.ScheduledAt)
	require.NoError(t, err)
	const count = 6
	jobs := make(chan domains.Schedule, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			r := req
			r.MediaID = fmt.Sprintf("asset-%d", i)
			m := domains.Media{ID: r.MediaID, ConnectionID: d.ConnectionID, Name: "same.txt", ContentType: "text/plain", Path: r.MediaID, Size: 12}
			job, err := s.CreateScheduleUpload(ctx, d.ConnectionID, r, next, "same", m, strings.Repeat("a", 64))
			jobs <- job
			errors <- err
		})
	}
	wg.Wait()
	close(jobs)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var first domains.Schedule
	for job := range jobs {
		if first.ID == "" {
			first = job
		}
		require.Equal(t, first.ID, job.ID)
		require.Equal(t, first.Request.MediaID, job.Request.MediaID)
	}
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&n))
	require.Equal(t, 1, n)
	errorCode(t, s.DeleteMedia(ctx, d.ConnectionID, first.Request.MediaID), "MEDIA_IN_USE")
	op, err := s.MaterializeSchedule(ctx, d.ConnectionID, first.ID, next, nil, AdmissionLimits{})
	require.NoError(t, err)
	require.Equal(t, first.Request.MediaID, op.Request.MediaID)
	require.NotEmpty(t, op.Request.RequestID)
	require.NoError(t, s.Close())
	s, err = Open(path, testKey)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	req.MediaID = "new-upload"
	m := domains.Media{ID: req.MediaID, ConnectionID: d.ConnectionID, Name: "same.txt", ContentType: "text/plain", Path: req.MediaID, Size: 12}
	got, found, err := s.LookupScheduleUpload(ctx, d.ConnectionID, req, "same", m, strings.Repeat("a", 64))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "completed", got.State)
	require.Equal(t, first.ID, got.ID)
	_, err = s.CreateScheduleUpload(ctx, d.ConnectionID, req, time.Time{}, "same", m, strings.Repeat("a", 64))
	require.NoError(t, err, "a duplicate keeps its completed schedule even without a future next time")
	_, err = s.CreateScheduleUpload(ctx, d.ConnectionID, req, next, "same", m, strings.Repeat("b", 64))
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, _, err = s.Enqueue(ctx, d.ConnectionID, textRequest("now"), "same", AdmissionLimits{})
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, err = s.GetMedia(ctx, d.ConnectionID, m.ID)
	errorCode(t, err, "NOT_FOUND")
	_, err = s.db.Exec(`CREATE TRIGGER reject_schedule BEFORE INSERT ON schedules BEGIN SELECT RAISE(ABORT,'synthetic schedule failure'); END`)
	require.NoError(t, err)
	_, err = s.CreateScheduleUpload(ctx, d.ConnectionID, req, next, "failure", m, strings.Repeat("a", 64))
	require.Error(t, err)
	_, err = s.GetMedia(ctx, d.ConnectionID, m.ID)
	errorCode(t, err, "NOT_FOUND")
}
