package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/pkg/sqlite"
	"golang.org/x/sys/unix"
)

// Latency contains cumulative, fixed-size buckets; it never retains requests.
type Latency struct {
	Count   uint64
	Seconds float64
	Buckets [9]uint64
}

var latencyBounds = [...]float64{.001, .005, .01, .05, .1, .5, 1, 5}

func (h *Latency) observe(elapsed time.Duration) {
	seconds := elapsed.Seconds()
	h.Count++
	h.Seconds += seconds
	for i, bound := range latencyBounds {
		if seconds <= bound {
			h.Buckets[i]++
		}
	}
	h.Buckets[len(latencyBounds)]++
}

type storageMetrics struct {
	mu                         sync.Mutex
	transactions               map[*sql.Tx]time.Time
	begin, transaction, commit Latency
	operations                 map[string]Latency
	errors                     map[string]uint64
}

type OperationalMetrics struct {
	Pool                       sql.DBStats
	Begin, Transaction, Commit Latency
	Operations                 map[string]Latency
	Errors                     map[string]uint64
}

// OperationalMetrics is a memory-only snapshot, safe even when storage is busy.
func (s *Store) OperationalMetrics() OperationalMetrics {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	m := OperationalMetrics{Pool: s.db.Stats(), Begin: s.metrics.begin, Transaction: s.metrics.transaction, Commit: s.metrics.commit, Operations: map[string]Latency{}, Errors: map[string]uint64{}}
	for k, v := range s.metrics.operations {
		m.Operations[k] = v
	}
	for k, v := range s.metrics.errors {
		m.Errors[k] = v
	}
	return m
}

func (s *Store) recordStorageError(err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, sql.ErrTxDone) {
		return
	}
	var domainError *domains.Error
	if errors.As(err, &domainError) {
		return
	}
	category := sqlite.ErrorCategory(err)
	if errors.Is(err, context.DeadlineExceeded) {
		category = "deadline"
	}
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	if s.metrics.errors == nil {
		s.metrics.errors = map[string]uint64{}
	}
	s.metrics.errors[category]++
}

func (s *Store) beginTx(ctx context.Context) (*sql.Tx, error) {
	start := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	s.metrics.mu.Lock()
	s.metrics.begin.observe(time.Since(start))
	if err == nil {
		if s.metrics.transactions == nil {
			s.metrics.transactions = map[*sql.Tx]time.Time{}
		}
		s.metrics.transactions[tx] = time.Now()
	}
	s.metrics.mu.Unlock()
	return tx, err
}

func (s *Store) completeTransaction(tx *sql.Tx) {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	if started, ok := s.metrics.transactions[tx]; ok {
		s.metrics.transaction.observe(time.Since(started))
		delete(s.metrics.transactions, tx)
	}
}

func (s *Store) commitTx(tx *sql.Tx) error {
	start := time.Now()
	err := tx.Commit()
	s.metrics.mu.Lock()
	s.metrics.commit.observe(time.Since(start))
	s.metrics.mu.Unlock()
	s.completeTransaction(tx)
	return err
}

func (s *Store) rollbackTx(tx *sql.Tx) error {
	err := tx.Rollback()
	s.completeTransaction(tx)
	return err
}

// Names come only from the finite wrappers below, never caller data or SQL.
func (s *Store) observePersistence(name string, start time.Time, err error) {
	s.metrics.mu.Lock()
	if s.metrics.operations == nil {
		s.metrics.operations = map[string]Latency{}
	}
	h := s.metrics.operations[name]
	h.observe(time.Since(start))
	s.metrics.operations[name] = h
	s.metrics.mu.Unlock()
	s.recordStorageError(err)
}

func (s *Store) AppendEvent(ctx context.Context, conn string, event domains.Event, targets []WebhookTarget) (created bool, err error) {
	start := time.Now()
	defer func() { s.observePersistence("append_event", start, err) }()
	return s.appendEvent(ctx, conn, event, targets)
}
func (s *Store) Enqueue(ctx context.Context, conn string, req domains.SendRequest, key string, limit int) (op domains.Operation, created bool, err error) {
	start := time.Now()
	defer func() { s.observePersistence("enqueue", start, err) }()
	return s.enqueue(ctx, conn, req, key, limit)
}
func (s *Store) ClaimOperations(ctx context.Context, limit int) (ops []domains.Operation, err error) {
	start := time.Now()
	defer func() { s.observePersistence("claim_outbox", start, err) }()
	return s.claimOperations(ctx, limit)
}
func (s *Store) FinishOperation(ctx context.Context, conn, id, state string, result *domains.SendResult, code, message string) (err error) {
	start := time.Now()
	defer func() { s.observePersistence("finish_outbox", start, err) }()
	return s.finishOperation(ctx, conn, id, state, result, code, message)
}
func (s *Store) ClaimDeliveries(ctx context.Context, limit int, at time.Time) (deliveries []domains.Delivery, err error) {
	start := time.Now()
	defer func() { s.observePersistence("claim_delivery", start, err) }()
	return s.claimDeliveries(ctx, limit, at)
}
func (s *Store) UpdateDelivery(ctx context.Context, conn, id, state string, next time.Time, lastError string) (err error) {
	start := time.Now()
	defer func() { s.observePersistence("update_delivery", start, err) }()
	return s.updateDelivery(ctx, conn, id, state, next, lastError)
}
func (s *Store) MaterializeSchedule(ctx context.Context, conn, id string, expected time.Time, next *time.Time, limit int) (op domains.Operation, err error) {
	start := time.Now()
	defer func() { s.observePersistence("schedule_occurrence", start, err) }()
	return s.materializeSchedule(ctx, conn, id, expected, next, limit)
}
func (s *Store) CreateScheduleIdempotent(ctx context.Context, conn string, request domains.SendRequest, next time.Time, key string) (schedule domains.Schedule, err error) {
	start := time.Now()
	defer func() { s.observePersistence("create_schedule", start, err) }()
	return s.createScheduleIdempotent(ctx, conn, request, next, key)
}

type DiskMetrics struct{ DatabaseBytes, WALBytes, SHMBytes, FreeBytes int64 }

// DiskMetrics only stats the known journal files and filesystem; it does not
// checkpoint SQLite or walk retained history. No paths escape this method.
func (s *Store) DiskMetrics() (m DiskMetrics, err error) {
	for _, file := range []struct {
		suffix string
		value  *int64
	}{{"", &m.DatabaseBytes}, {"-wal", &m.WALBytes}, {"-shm", &m.SHMBytes}} {
		info, e := os.Stat(s.dbPath + file.suffix)
		if os.IsNotExist(e) && file.suffix != "" {
			continue
		}
		if e != nil {
			return m, e
		}
		*file.value = info.Size()
	}
	var stat unix.Statfs_t
	if err = unix.Statfs(s.dbPath, &stat); err != nil {
		return m, err
	}
	m.FreeBytes = int64(stat.Bavail) * int64(stat.Bsize)
	return m, nil
}
