package usecase

import (
	"errors"
	"sync"
	"time"

	"github.com/mimalef70/gobale/src/domains"
)

// Metrics contains bounded process-local worker statistics, never account IDs,
// destinations, payloads or credentials. Queue depths/ages come from storage.
type Metrics struct {
	SendAttempts           uint64            `json:"send_attempts"`
	SendUnknown            uint64            `json:"send_unknown"`
	SendDurationSeconds    float64           `json:"send_duration_seconds"`
	SendDurationBuckets    map[string]uint64 `json:"send_duration_buckets"`
	WebhookAttempts        uint64            `json:"webhook_attempts"`
	WebhookFailures        uint64            `json:"webhook_failures"`
	WebhookDurationSeconds float64           `json:"webhook_duration_seconds"`
	WebhookDurationBuckets map[string]uint64 `json:"webhook_duration_buckets"`
	WorkerErrors           uint64            `json:"worker_errors"`
	LastWorkerErrorCode    string            `json:"last_worker_error_code,omitempty"`
}
type workerMetrics struct {
	mu     sync.Mutex
	values Metrics
}

func (s *Service) WorkerMetrics() Metrics {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	value := s.metrics.values
	value.SendDurationBuckets = cloneBuckets(value.SendDurationBuckets)
	value.WebhookDurationBuckets = cloneBuckets(value.WebhookDurationBuckets)
	return value
}
func cloneBuckets(source map[string]uint64) map[string]uint64 {
	result := map[string]uint64{}
	for key, value := range source {
		result[key] = value
	}
	return result
}
func observeBuckets(buckets *map[string]uint64, seconds float64) {
	if *buckets == nil {
		*buckets = map[string]uint64{}
	}
	for _, bound := range []struct {
		label   string
		seconds float64
	}{{"0.01", .01}, {"0.1", .1}, {"1", 1}, {"5", 5}, {"10", 10}, {"40", 40}} {
		if seconds <= bound.seconds {
			(*buckets)[bound.label]++
		}
	}
	(*buckets)["+Inf"]++
}
func (s *Service) observeSend(start time.Time, unknown bool) {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	m := &s.metrics.values
	seconds := time.Since(start).Seconds()
	m.SendAttempts++
	if unknown {
		m.SendUnknown++
	}
	m.SendDurationSeconds += seconds
	observeBuckets(&m.SendDurationBuckets, seconds)
}
func (s *Service) observeWebhook(start time.Time, failed bool) {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	m := &s.metrics.values
	seconds := time.Since(start).Seconds()
	m.WebhookAttempts++
	if failed {
		m.WebhookFailures++
	}
	m.WebhookDurationSeconds += seconds
	observeBuckets(&m.WebhookDurationBuckets, seconds)
}
func (s *Service) workerError(err error) {
	if err == nil {
		return
	}
	// Context cancellation is expected at shutdown, not a worker failure.
	if errors.Is(err, s.ctx.Err()) && s.ctx.Err() != nil {
		return
	}
	code := "STORAGE_ERROR"
	var de *domains.Error
	if errors.As(err, &de) {
		code = safeCode(de.Code)
	}
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	s.metrics.values.WorkerErrors++
	s.metrics.values.LastWorkerErrorCode = code
}
