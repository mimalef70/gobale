package rest

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"golang.org/x/sys/unix"
)

const metricsInterval = 5 * time.Second
const metricsMaxAge = 15 * time.Second

type metricsSnapshot struct {
	queue                                                                map[string]int64
	disk                                                                 storage.DiskMetrics
	mediaBytes, mediaFreeBytes                                           int64
	queueAt, diskAt, mediaAt, mediaDiskAt                                time.Time
	queueFailed, diskFailed, mediaFailed, mediaDiskFailed, mediaScanning bool
}

type serverMetrics struct {
	mu                               sync.RWMutex
	once                             sync.Once
	snapshot                         metricsSnapshot
	panics, timeouts, sampleFailures atomic.Uint64
}

// StartMetrics starts one bounded sampler with the process lifecycle. Scrapes
// never wait for SQLite, stat a file, or traverse the private media directory.
// Constructors do not start goroutines, so test servers remain easy to close.
func (s *Server) StartMetrics(ctx context.Context) {
	s.operational.once.Do(func() {
		go func() {
			scan := &mediaScanner{}
			defer scan.close()
			ticker := time.NewTicker(metricsInterval)
			defer ticker.Stop()
			for ctx.Err() == nil {
				s.sampleMetrics(ctx, scan)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

func (s *Server) sampleMetrics(ctx context.Context, scan *mediaScanner) {
	queueCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	queue, queueErr := s.store.Stats(queueCtx)
	cancel()
	queueAt := time.Now().UTC()
	disk, diskErr := s.store.DiskMetrics()
	diskAt := time.Now().UTC()
	// MediaRoot may be a separate mount. One filesystem stat per sample keeps
	// its available capacity independent of the SQLite filesystem and scans.
	var mediaDisk unix.Statfs_t
	mediaDiskErr := unix.Statfs(s.opts.MediaRoot, &mediaDisk)
	mediaDiskAt := time.Now().UTC()
	mediaCtx, mediaCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	mediaBytes, complete, mediaErr := scan.step(mediaCtx, s.opts.MediaRoot)
	mediaCancel()
	mediaAt := time.Now().UTC()
	s.operational.mu.Lock()
	defer s.operational.mu.Unlock()
	v := &s.operational.snapshot
	v.queueFailed, v.diskFailed, v.mediaFailed = queueErr != nil, diskErr != nil, mediaErr != nil
	v.mediaDiskFailed = mediaDiskErr != nil
	v.mediaScanning = !complete && mediaErr == nil
	if queueErr == nil {
		v.queue, v.queueAt = queue, queueAt
	}
	if diskErr == nil {
		v.disk, v.diskAt = disk, diskAt
	}
	if mediaDiskErr == nil {
		v.mediaFreeBytes = int64(mediaDisk.Bavail) * int64(mediaDisk.Bsize)
		v.mediaDiskAt = mediaDiskAt
	}
	if mediaErr == nil && complete {
		v.mediaBytes, v.mediaAt = mediaBytes, mediaAt
	}
	for _, err := range []error{queueErr, diskErr, mediaErr, mediaDiskErr} {
		if err != nil {
			s.operational.sampleFailures.Add(1)
		}
	}
}

func (s *Server) metricSnapshot() metricsSnapshot {
	s.operational.mu.RLock()
	defer s.operational.mu.RUnlock()
	v := s.operational.snapshot
	v.queue = make(map[string]int64, len(s.operational.snapshot.queue))
	for k, n := range s.operational.snapshot.queue {
		v.queue[k] = n
	}
	return v
}

func metric(b *strings.Builder, name, kind string, value any) {
	fmt.Fprintf(b, "# TYPE gobale_%s %s\ngobale_%s %v\n", name, kind, name, value)
}

func latencyMetric(b *strings.Builder, name, labels string, h storage.Latency) {
	if labels == "" {
		fmt.Fprintf(b, "# TYPE gobale_%s histogram\n", name)
	}
	for i, bound := range []string{"0.001", "0.005", "0.01", "0.05", "0.1", "0.5", "1", "5", "+Inf"} {
		fmt.Fprintf(b, "gobale_%s_bucket{%sle=%q} %d\n", name, labels, bound, h.Buckets[i])
	}
	if labels != "" {
		labels = "{" + strings.TrimSuffix(labels, ",") + "}"
	}
	fmt.Fprintf(b, "gobale_%s_sum%s %g\ngobale_%s_count%s %d\n", name, labels, h.Seconds, name, labels, h.Count)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *Server) metrics(c fiber.Ctx) error {
	var b strings.Builder
	v := s.metricSnapshot()
	for _, k := range sortedKeys(v.queue) {
		metric(&b, k, "gauge", v.queue[k])
	}
	for _, item := range []struct {
		name   string
		at     time.Time
		failed bool
	}{{"queue", v.queueAt, v.queueFailed}, {"disk", v.diskAt, v.diskFailed}, {"media", v.mediaAt, v.mediaFailed}, {"media_disk", v.mediaDiskAt, v.mediaDiskFailed}} {
		stamp, stale := int64(0), 0
		if !item.at.IsZero() {
			stamp = item.at.Unix()
		}
		if item.at.IsZero() || item.failed || time.Since(item.at) > metricsMaxAge {
			stale = 1
		}
		fmt.Fprintf(&b, "gobale_metrics_snapshot_timestamp_seconds{component=%q} %d\ngobale_metrics_snapshot_stale{component=%q} %d\n", item.name, stamp, item.name, stale)
	}
	metric(&b, "database_bytes", "gauge", v.disk.DatabaseBytes)
	metric(&b, "database_wal_bytes", "gauge", v.disk.WALBytes)
	metric(&b, "database_shm_bytes", "gauge", v.disk.SHMBytes)
	metric(&b, "storage_free_bytes", "gauge", v.disk.FreeBytes)
	metric(&b, "media_free_bytes", "gauge", v.mediaFreeBytes)
	metric(&b, "media_bytes", "gauge", v.mediaBytes)
	scanning := 0
	if v.mediaScanning {
		scanning = 1
	}
	metric(&b, "media_scan_in_progress", "gauge", scanning)
	metric(&b, "metrics_sample_failures_total", "counter", s.operational.sampleFailures.Load())
	metric(&b, "http_panics_total", "counter", s.operational.panics.Load())
	metric(&b, "http_timeouts_total", "counter", s.operational.timeouts.Load())
	db := s.store.OperationalMetrics()
	metric(&b, "db_pool_wait_total", "counter", db.Pool.WaitCount)
	metric(&b, "db_pool_wait_seconds_total", "counter", db.Pool.WaitDuration.Seconds())
	metric(&b, "db_connections_in_use", "gauge", db.Pool.InUse)
	metric(&b, "db_connections_idle", "gauge", db.Pool.Idle)
	latencyMetric(&b, "db_begin_duration_seconds", "", db.Begin)
	latencyMetric(&b, "db_transaction_duration_seconds", "", db.Transaction)
	latencyMetric(&b, "db_commit_duration_seconds", "", db.Commit)
	fmt.Fprint(&b, "# TYPE gobale_persistence_duration_seconds histogram\n# TYPE gobale_storage_errors_total counter\n")
	for _, name := range sortedKeys(db.Operations) {
		latencyMetric(&b, "persistence_duration_seconds", fmt.Sprintf("operation=%q,", name), db.Operations[name])
	}
	for _, category := range sortedKeys(db.Errors) {
		fmt.Fprintf(&b, "gobale_storage_errors_total{category=%q} %d\n", category, db.Errors[category])
	}
	m := s.service.WorkerMetrics()
	metric(&b, "send_attempts_total", "counter", m.SendAttempts)
	metric(&b, "send_unknown_total", "counter", m.SendUnknown)
	metric(&b, "webhook_attempts_total", "counter", m.WebhookAttempts)
	metric(&b, "webhook_failures_total", "counter", m.WebhookFailures)
	metric(&b, "worker_errors_total", "counter", m.WorkerErrors)
	metric(&b, "send_workers_busy", "gauge", m.SendBusy)
	metric(&b, "webhook_workers_busy", "gauge", m.WebhookBusy)
	metric(&b, "reconnect_workers_busy", "gauge", m.ReconnectBusy)
	metric(&b, "reconnect_attempts_total", "counter", m.ReconnectAttempts)
	metric(&b, "reconnect_failures_total", "counter", m.ReconnectFailures)
	for _, h := range []struct {
		name    string
		buckets map[string]uint64
		sum     float64
		count   uint64
	}{
		{"send_duration_seconds", m.SendDurationBuckets, m.SendDurationSeconds, m.SendAttempts},
		{"webhook_duration_seconds", m.WebhookDurationBuckets, m.WebhookDurationSeconds, m.WebhookAttempts},
		{"reconnect_duration_seconds", m.ReconnectDurationBuckets, m.ReconnectDurationSeconds, m.ReconnectAttempts},
	} {
		fmt.Fprintf(&b, "# TYPE gobale_%s histogram\n", h.name)
		for _, bound := range []string{"0.01", "0.1", "1", "5", "10", "40", "+Inf"} {
			fmt.Fprintf(&b, "gobale_%s_bucket{le=%q} %d\n", h.name, bound, h.buckets[bound])
		}
		fmt.Fprintf(&b, "gobale_%s_sum %g\ngobale_%s_count %d\n", h.name, h.sum, h.name, h.count)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	metric(&b, "go_goroutines", "gauge", runtime.NumGoroutine())
	metric(&b, "go_heap_bytes", "gauge", mem.HeapAlloc)
	c.Set("Content-Type", "text/plain; version=0.0.4")
	return c.SendString(b.String())
}
