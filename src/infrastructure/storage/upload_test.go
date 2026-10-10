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

func TestUploadEnqueueAtomicRegistrationPinsAndRestartIdempotency(t *testing.T) {
	s, path := testStore(t)
	ctx := context.Background()
	d := device(t, s, "one")
	req := textRequest("caption")
	req.Kind, req.MediaID = "file", "file-one"
	m := domains.Media{ID: req.MediaID, ConnectionID: d.ConnectionID, Name: "one.txt", ContentType: "text/plain", Size: 12, Path: "owned-one"}
	op, err := s.EnqueueUpload(ctx, d.ConnectionID, req, "key", m, strings.Repeat("a", 64), AdmissionLimits{})
	require.NoError(t, err)
	require.NotEmpty(t, op.Request.RequestID)
	errorCode(t, s.DeleteMedia(ctx, d.ConnectionID, m.ID), "MEDIA_IN_USE")
	claimed, err := s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "synthetic ambiguous result"))
	require.NoError(t, s.Close())
	s, err = Open(path, testKey)
	require.NoError(t, err)
	defer s.Close()
	req.MediaID, m.ID, m.Path = "retry-file", "retry-file", "owned-retry"
	again, err := s.EnqueueUpload(ctx, d.ConnectionID, req, "key", m, strings.Repeat("a", 64), AdmissionLimits{})
	require.NoError(t, err)
	require.Equal(t, op.ID, again.ID)
	require.Equal(t, op.Request.RequestID, again.Request.RequestID)
	require.Equal(t, "unknown", again.State, "multipart retry must not resend ambiguous accepted work")
	_, err = s.GetMedia(ctx, d.ConnectionID, "retry-file")
	errorCode(t, err, "NOT_FOUND")
	_, err = s.EnqueueUpload(ctx, d.ConnectionID, req, "key", m, strings.Repeat("b", 64), AdmissionLimits{})
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, err = s.GetMedia(ctx, d.ConnectionID, "retry-file")
	errorCode(t, err, "NOT_FOUND")
	// Direct sends and schedules retain the same connection-owned namespace.
	_, _, err = s.Enqueue(ctx, d.ConnectionID, textRequest("other"), "key", AdmissionLimits{})
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, err = s.CreateScheduleIdempotent(ctx, d.ConnectionID, textRequest("later"), time.Now().Add(time.Hour), "key")
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
}

func TestConcurrentUploadRetriesPersistOnlyOriginalAsset(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "one")
	const count = 8
	results := make(chan domains.Operation, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Go(func() {
			req := textRequest("caption")
			req.Kind, req.MediaID = "file", fmt.Sprintf("file-%d", i)
			media := domains.Media{ID: req.MediaID, ConnectionID: d.ConnectionID, Path: req.MediaID, Name: "same.txt", ContentType: "text/plain", Size: 12}
			op, err := s.EnqueueUpload(ctx, d.ConnectionID, req, "same-key", media, strings.Repeat("c", 64), AdmissionLimits{})
			results <- op
			errors <- err
		})
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var original domains.Operation
	for op := range results {
		if original.ID == "" {
			original = op
		}
		require.Equal(t, original.ID, op.ID)
		require.Equal(t, original.Request.RequestID, op.Request.RequestID)
		require.Equal(t, original.Request.MediaID, op.Request.MediaID)
	}
	var mediaCount, operationCount int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&mediaCount))
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&operationCount))
	require.Equal(t, 1, mediaCount)
	require.Equal(t, 1, operationCount)
}

func TestUploadEnqueueRollsBackOnPersistenceOrAdmissionFailure(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "one")
	req := textRequest("caption")
	req.Kind, req.MediaID = "file", "file-one"
	m := domains.Media{ID: req.MediaID, ConnectionID: d.ConnectionID, Name: "one.txt", ContentType: "text/plain", Size: 12, Path: "owned-one"}
	_, err := s.db.Exec(`CREATE TRIGGER reject_upload BEFORE INSERT ON operations BEGIN SELECT RAISE(ABORT,'synthetic operation persistence failure'); END`)
	require.NoError(t, err)
	_, err = s.EnqueueUpload(ctx, d.ConnectionID, req, "key", m, strings.Repeat("a", 64), AdmissionLimits{})
	require.Error(t, err)
	_, err = s.GetMedia(ctx, d.ConnectionID, m.ID)
	errorCode(t, err, "NOT_FOUND")
	_, err = s.db.Exec(`DROP TRIGGER reject_upload`)
	require.NoError(t, err)
	_, _, err = s.Enqueue(ctx, d.ConnectionID, textRequest("fills queue"), "existing", AdmissionLimits{})
	require.NoError(t, err)
	_, err = s.EnqueueUpload(ctx, d.ConnectionID, req, "key", m, strings.Repeat("a", 64), AdmissionLimits{Global: 1, Connection: 1})
	errorCode(t, err, "QUEUE_FULL")
	_, err = s.GetMedia(ctx, d.ConnectionID, m.ID)
	errorCode(t, err, "NOT_FOUND")
	_, err = s.CreateScheduleIdempotent(ctx, d.ConnectionID, textRequest("later"), time.Now().Add(time.Hour), "schedule-key")
	require.NoError(t, err)
	_, err = s.EnqueueUpload(ctx, d.ConnectionID, req, "schedule-key", m, strings.Repeat("a", 64), AdmissionLimits{})
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
}
