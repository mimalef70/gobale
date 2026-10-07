package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func TestConnectionAdmissionIncludesQueuedSendingAndUnknown(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a, b := device(t, s, "limited"), device(t, s, "independent")
	limits := AdmissionLimits{Global: 10, Connection: 3}
	var first domains.Operation
	for i := range 3 {
		op, _, err := s.Enqueue(ctx, a.ConnectionID, textRequest(fmt.Sprint(i)), fmt.Sprint(i), limits)
		require.NoError(t, err)
		if i == 0 {
			first = op
		}
	}
	claimed, err := s.ClaimOperations(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, s.FinishOperation(ctx, a.ConnectionID, claimed[0].ID, "unknown", nil, "SEND_UNKNOWN", "synthetic"))
	claimed, err = s.ClaimOperations(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, _, err = s.Enqueue(ctx, a.ConnectionID, textRequest("new"), "new", limits)
	errorCode(t, err, "CONNECTION_QUEUE_FULL")
	var full *domains.Error
	require.ErrorAs(t, err, &full)
	require.Equal(t, 429, full.HTTP)
	require.True(t, full.Retryable)
	// Existing identical work is inspectable at capacity; changed content still conflicts.
	again, created, err := s.Enqueue(ctx, a.ConnectionID, textRequest("0"), "0", limits)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, again.ID)
	_, _, err = s.Enqueue(ctx, a.ConnectionID, textRequest("changed"), "0", limits)
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, _, err = s.Enqueue(ctx, b.ConnectionID, textRequest("independent"), "0", limits)
	require.NoError(t, err)
	// Only terminal resolution frees a slot; no ambiguous work is deleted or retried.
	require.NoError(t, s.FinishOperation(ctx, a.ConnectionID, claimed[0].ID, "succeeded", &domains.SendResult{MessageID: "42"}, "", ""))
	_, _, err = s.Enqueue(ctx, a.ConnectionID, textRequest("new"), "new", limits)
	require.NoError(t, err)
	retained, err := s.GetOperation(ctx, a.ConnectionID, first.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", retained.State)
}

func TestConnectionAdmissionIsAtomicUnderConcurrentWriters(t *testing.T) {
	s, _ := testStore(t)
	d := device(t, s, "concurrent")
	var wg sync.WaitGroup
	results := make(chan error, 30)
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.Enqueue(context.Background(), d.ConnectionID, textRequest("synthetic"), fmt.Sprint(i), AdmissionLimits{Global: 50, Connection: 5})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
			continue
		}
		var de *domains.Error
		require.True(t, errors.As(err, &de))
		require.Equal(t, "CONNECTION_QUEUE_FULL", de.Code)
		rejected++
	}
	require.Equal(t, 5, accepted)
	require.Equal(t, 25, rejected)
}

func TestConnectionAdmissionDefaultsAndAliasReuse(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "reused")
	for i := range 100 {
		_, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("synthetic"), fmt.Sprint(i), AdmissionLimits{})
		require.NoError(t, err)
	}
	_, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("over limit"), "overflow", AdmissionLimits{})
	errorCode(t, err, "CONNECTION_QUEUE_FULL")
	claimed, err := s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, claimed[0].ID, "unknown", nil, "SEND_UNKNOWN", "synthetic"))
	require.NoError(t, s.DeleteDevice(ctx, d.ConnectionID))
	replacement := device(t, s, "reused")
	_, _, err = s.Enqueue(ctx, replacement.ConnectionID, textRequest("new owner"), "0", AdmissionLimits{Global: 1000, Connection: 1})
	require.NoError(t, err)
	_, _, err = s.Enqueue(ctx, replacement.ConnectionID, textRequest("over limit"), "1", AdmissionLimits{Global: 1000, Connection: 1})
	errorCode(t, err, "CONNECTION_QUEUE_FULL")
}

func TestConnectionAdmissionDoesNotConsumeScheduledOccurrence(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "scheduled")
	limits := AdmissionLimits{Global: 10, Connection: 1}
	_, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("prior"), "prior", limits)
	require.NoError(t, err)
	due := time.Now().UTC().Truncate(time.Millisecond)
	next := due.Add(time.Hour)
	job, err := s.CreateSchedule(ctx, d.ConnectionID, textRequest("scheduled"), due)
	require.NoError(t, err)
	_, err = s.MaterializeSchedule(ctx, d.ConnectionID, job.ID, due, &next, limits)
	errorCode(t, err, "CONNECTION_QUEUE_FULL")
	unchanged, err := s.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	require.Equal(t, due, unchanged.NextAt)
	require.Equal(t, "active", unchanged.State)
	require.Zero(t, unchanged.Count)
	occurrences, err := s.ListScheduleOccurrences(ctx, d.ConnectionID, job.ID, "", 10, 0)
	require.NoError(t, err)
	require.Empty(t, occurrences)
	claimed, err := s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, claimed[0].ID, "succeeded", &domains.SendResult{MessageID: "42"}, "", ""))
	op, err := s.MaterializeSchedule(ctx, d.ConnectionID, job.ID, due, &next, limits)
	require.NoError(t, err)
	require.Equal(t, job.ID, op.ScheduleID)
	after, err := s.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	require.Equal(t, next, after.NextAt)
	require.EqualValues(t, 1, after.Count)
}
