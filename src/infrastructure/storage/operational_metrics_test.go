package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOperationalMetricsObservePersistenceAndPoolWait(t *testing.T) {
	s, _ := testStore(t)
	d := device(t, s, "metrics")
	ctx := context.Background()
	_, err := s.AppendEvent(ctx, d.ConnectionID, event("metric-event", "metric-cursor"), nil)
	require.NoError(t, err)
	m := s.OperationalMetrics()
	require.EqualValues(t, 1, m.Operations["append_event"].Count)
	require.EqualValues(t, 1, m.Operations["append_event"].Buckets[8])
	require.Positive(t, m.Commit.Count)
	require.Equal(t, m.Transaction.Count, m.Commit.Count)
	conn, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err = s.AppendEvent(waitCtx, d.ConnectionID, event("blocked", "later"), nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	m = s.OperationalMetrics()
	require.Positive(t, m.Pool.WaitCount)
	require.Positive(t, m.Pool.WaitDuration)
	require.EqualValues(t, 1, m.Errors["deadline"])
	require.EqualValues(t, 2, m.Operations["append_event"].Count)
}

func TestOperationalMetricsClassifySQLiteWithoutErrorText(t *testing.T) {
	s, _ := testStore(t)
	d := device(t, s, "metrics")
	_, err := s.db.Exec(`CREATE TRIGGER metric_failure BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'private payload must stay private'); END`)
	require.NoError(t, err)
	_, err = s.AppendEvent(context.Background(), d.ConnectionID, event("rejected", "cp"), nil)
	require.Error(t, err)
	m := s.OperationalMetrics()
	require.EqualValues(t, 1, m.Errors["constraint"])
	require.Len(t, m.Errors, 1)
	require.Len(t, s.metrics.transactions, 0, "rollback must release timing state")
	disk, err := s.DiskMetrics()
	require.NoError(t, err)
	require.Positive(t, disk.DatabaseBytes)
	require.Positive(t, disk.FreeBytes)
}
