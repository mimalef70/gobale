package rest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/mediafile"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/mimalef70/gobale/src/pkg/sqlite"
	"github.com/mimalef70/gobale/src/usecase"
	"golang.org/x/sys/unix"
)

// Optional mixed workload through real REST handlers and durable storage. Only
// the provider is fake. No customer accounts, network endpoints or data are used.
func TestOptionalCapacity(t *testing.T) {
	if os.Getenv("GOBALE_SOAK_DURATION") == "" {
		t.Skip("set GOBALE_SOAK_DURATION for the isolated mixed capacity workload")
	}
	cfg := readCapacityConfig(t)
	ctx := context.Background()
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	root := t.TempDir()
	dbPath := filepath.Join(root, "gateway.db")
	receiver, err := sql.Open(sqlite.DriverName, sqlite.FormatChatStorageURI((&url.URL{Scheme: "file", Path: filepath.Join(root, "receiver.db")}).String(), false, true))
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	receiver.SetMaxOpenConns(1)
	for _, query := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `PRAGMA cache_size=-2048`, `CREATE TABLE receipts(target TEXT,event_id TEXT,account INTEGER,sequence INTEGER,PRIMARY KEY(target,event_id))`, `CREATE INDEX receipts_order ON receipts(target,account,sequence)`, `CREATE TABLE sends(rid TEXT PRIMARY KEY,account TEXT)`, `CREATE TABLE faults(event_id TEXT PRIMARY KEY)`} {
		if _, err = receiver.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	var bad, completed, duplicates atomic.Int64
	fastLatency := &capacityHistogram{}
	admissionLatency := &capacityHistogram{}
	var firstError atomic.Pointer[string]
	recordError := func(err error) {
		if err != nil {
			v := err.Error()
			firstError.CompareAndSwap(nil, &v)
		}
	}
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, 128<<10))
		if e != nil {
			bad.Add(1)
			w.WriteHeader(400)
			return
		}
		mac := hmac.New(sha256.New, []byte("synthetic-capacity-secret"))
		mac.Write(body)
		var ev domains.Event
		var p struct {
			Sequence int64 `json:"sequence"`
			Account  int   `json:"account"`
			Created  int64 `json:"created_ns"`
			Healthy  bool  `json:"healthy"`
		}
		if r.Header.Get("X-Hub-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || json.Unmarshal(body, &ev) != nil || json.Unmarshal(ev.Payload, &p) != nil || p.Account < 0 || p.Account >= cfg.accounts || ev.SessionID != fmt.Sprintf("capacity-%d", p.Account) || ev.AccountID != strconv.Itoa(100000+p.Account) || p.Sequence < 1 {
			bad.Add(1)
			w.WriteHeader(400)
			return
		}
		target := strings.TrimPrefix(r.URL.Path, "/")
		if target == "slow" {
			time.Sleep(150 * time.Millisecond)
		}
		if target == "flaky" && p.Sequence%11 == 0 {
			result, e := receiver.Exec(`INSERT INTO faults(event_id) VALUES(?) ON CONFLICT DO NOTHING`, ev.ID)
			if e != nil {
				recordError(e)
				w.WriteHeader(500)
				return
			}
			n, _ := result.RowsAffected()
			if n > 0 {
				w.WriteHeader(503)
				return
			}
		}
		tx, e := receiver.BeginTx(r.Context(), nil)
		if e != nil {
			recordError(e)
			w.WriteHeader(500)
			return
		}
		defer tx.Rollback()
		var previous, existing int64
		if e = tx.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM receipts WHERE target=? AND account=?`, target, p.Account).Scan(&previous); e != nil {
			recordError(e)
			w.WriteHeader(500)
			return
		}
		if e = tx.QueryRow(`SELECT COUNT(*) FROM receipts WHERE target=? AND event_id=?`, target, ev.ID).Scan(&existing); e != nil {
			recordError(e)
			w.WriteHeader(500)
			return
		}
		if existing == 0 && previous >= p.Sequence {
			bad.Add(1)
		}
		result, e := tx.Exec(`INSERT INTO receipts(target,event_id,account,sequence) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, target, ev.ID, p.Account, p.Sequence)
		if e != nil {
			recordError(e)
			w.WriteHeader(500)
			return
		}
		n, e := result.RowsAffected()
		if e != nil {
			recordError(e)
			w.WriteHeader(500)
			return
		}
		if e = tx.Commit(); e != nil {
			recordError(e)
			w.WriteHeader(500)
			return
		}
		if n == 0 {
			duplicates.Add(1)
		} else if target == "fast" {
			completed.Add(1)
			if p.Healthy {
				fastLatency.observe(time.Since(time.Unix(0, p.Created)))
			}
		}
		w.WriteHeader(204)
	}))
	defer sink.Close()
	st, err := storage.Open(dbPath, bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mediaRoot := filepath.Join(root, "media")
	manager, e := mediafile.Open(mediaRoot, st.MediaFileRegistered)
	if e != nil {
		t.Fatal(e)
	}
	if e = manager.Sweep(runCtx, 256); e != nil {
		manager.Close()
		t.Fatal(e)
	}
	if e = manager.CleanupLegacy(runCtx, 256); e != nil {
		manager.Close()
		t.Fatal(e)
	}
	mediaDone := make(chan struct{})
	go func() { defer close(mediaDone); manager.Run(runCtx) }()
	defer func() {
		cancelRun()
		<-mediaDone
		if e := manager.Close(); e != nil {
			t.Error(e)
		}
	}()
	devices := make([]domains.Device, cfg.accounts)
	for i := range devices {
		d, e := st.CreateDevice(ctx, fmt.Sprintf("capacity-%d", i))
		if e != nil {
			t.Fatal(e)
		}
		devices[i] = d
		if e = st.SaveSession(ctx, d.ConnectionID, &domains.Session{UserID: strconv.Itoa(100000 + i), Token: "synthetic-capacity-session"}); e != nil {
			t.Fatal(e)
		}
		if i%10 < 2 {
			target := sink.URL + "/slow"
			if i%10 == 1 {
				target = sink.URL + "/flaky"
			}
			secret := "synthetic-capacity-secret"
			if _, e = st.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{URL: &target, Secret: &secret}); e != nil {
				t.Fatal(e)
			}
		}
	}
	var clientsMu sync.Mutex
	clients := make(map[string]*capacityClient, cfg.accounts)
	svc := usecase.New(st, usecase.Options{MergeGlobal: true, SendWorkers: 4, WebhookWorkers: 8, ReconnectWorkers: 4, QueueLimit: capacityQueueLimit, ConnectionQueueLimit: capacityConnectionQueueLimit, PollInterval: 500 * time.Millisecond, GlobalWebhooks: []storage.WebhookTarget{{URL: sink.URL + "/fast", Secret: "synthetic-capacity-secret"}}}, func(d domains.Device) domains.Client {
		c := &capacityClient{account: d.AccountID}
		c.sendFn = func(r domains.SendRequest) (domains.SendResult, error) {
			_, e := receiver.Exec(`INSERT INTO sends(rid,account) VALUES(?,?)`, r.RequestID, d.AccountID)
			if e != nil {
				recordError(e)
				return domains.SendResult{}, e
			}
			return domains.SendResult{MessageID: r.RequestID, Date: time.Now().UTC()}, nil
		}
		clientsMu.Lock()
		clients[d.ID] = c
		clientsMu.Unlock()
		return c
	})
	if err = svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if e := svc.Close(stop); e != nil {
			t.Error(e)
		}
	}()
	srv, err := New(svc, st, Options{BasicAuth: "capacity:synthetic", MediaRoot: mediaRoot, MediaManager: manager, MaxMediaBytes: 2 << 20, SendWait: 40 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv.StartMetrics(runCtx)
	defer func() {
		shutdown, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if e := srv.App.ShutdownWithContext(shutdown); e != nil {
			t.Error(e)
		}
	}()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ready := 0
		clientsMu.Lock()
		for _, c := range clients {
			if c.Status().Transport == "connected" {
				ready++
			}
		}
		clientsMu.Unlock()
		if ready == cfg.accounts {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d synthetic clients connected", ready, cfg.accounts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	type job struct {
		lane     int
		sequence int64
		healthy  bool
		created  time.Time
	}
	counters := make([]capacityCounter, 5)
	queues := make([]chan job, 16)
	var workers sync.WaitGroup
	instances := make(map[string]string, len(devices))
	for _, d := range devices {
		instances[d.ID] = d.InstanceToken()
	}
	request := func(method, path, alias, key string, body []byte, contentType string) (int, []byte, error) {
		req := httptest.NewRequestWithContext(runCtx, method, path, bytes.NewReader(body))
		req.SetBasicAuth("capacity", "synthetic")
		req.Header.Set("X-Device-Id", alias)
		req.Header.Set("X-Device-Instance", instances[alias])
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Idempotency-Key", key)
		response, e := srv.App.Test(req, fiber.TestConfig{Timeout: 50 * time.Second})
		if e != nil {
			return 0, nil, e
		}
		defer response.Body.Close()
		b, e := io.ReadAll(response.Body)
		return response.StatusCode, b, e
	}
	for i := range queues {
		queues[i] = make(chan job, 64)
		q := queues[i]
		workers.Go(func() {
			for j := range q {
				if runCtx.Err() != nil {
					return
				}
				account := int((j.sequence + int64(cfg.seed)) % int64(cfg.accounts))
				d := devices[account]
				ok := false
				switch j.lane {
				case 0:
					clientsMu.Lock()
					c := clients[d.ID]
					clientsMu.Unlock()
					payload, _ := json.Marshal(map[string]any{"kind": "text", "message": "synthetic capacity text", "sequence": j.sequence, "account": account, "created_ns": j.created.UnixNano(), "healthy": j.healthy})
					event := domains.Event{ID: fmt.Sprintf("capacity-event-%d", j.sequence), Type: "message", Peer: domains.Peer{Type: "user", ID: "77"}, SenderID: "77", Direction: "incoming", MessageID: strconv.FormatInt(j.sequence, 10), Time: time.Now().UTC(), Payload: payload}
					before := time.Now()
					e := c.emit(runCtx, event)
					ok = e == nil
					if ok && j.healthy {
						admissionLatency.observe(time.Since(before))
					}
					if e != nil {
						recordError(e)
					}
				case 1, 2:
					body := map[string]any{"peer": domains.Peer{Type: "user", ID: "77"}, "message": "synthetic capacity send"}
					path := "/send/message"
					key := fmt.Sprintf("capacity-%d-%d", j.lane, j.sequence)
					if j.lane == 2 {
						path = "/send/schedules"
						body["scheduled_at"] = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
						body["timezone"] = "UTC"
					}
					encoded, _ := json.Marshal(body)
					status, _, e := request("POST", path, d.ID, key, encoded, "application/json")
					ok = e == nil && (status == 200 || status == 202)
					if e != nil {
						recordError(e)
					}
					// Duplicate admission must preserve the original operation/schedule. Changed
					// content must conflict, without producing another provider invocation.
					if ok && j.sequence%20 == 0 {
						status, _, e = request("POST", path, d.ID, key, encoded, "application/json")
						if e != nil || status >= 300 {
							bad.Add(1)
						}
						body["message"] = "changed synthetic content"
						encoded, _ = json.Marshal(body)
						status, _, e = request("POST", path, d.ID, key, encoded, "application/json")
						if e != nil || status != 409 {
							bad.Add(1)
						}
					}
				case 3:
					paths := []string{"/events?search=synthetic&limit=20", "/send/operations?limit=20", "/send/schedules?limit=20"}
					status, _, e := request("GET", paths[j.sequence%3], d.ID, "", nil, "application/json")
					ok = e == nil && status == 200
					if e != nil {
						recordError(e)
					}
				case 4:
					status, _, e := request("POST", "/media", d.ID, "", make([]byte, 1<<20), "application/octet-stream")
					ok = e == nil && status == 201
					if e != nil {
						recordError(e)
					}
				}
				if ok {
					counters[j.lane].accepted.Add(1)
					if j.healthy {
						counters[j.lane].healthyAccepted.Add(1)
					}
				} else {
					counters[j.lane].rejected.Add(1)
				}
			}
		})
	}
	start := time.Now()
	warmEnd := start.Add(cfg.warmup)
	end := warmEnd.Add(cfg.duration)
	burstStart := warmEnd.Add(cfg.duration / 3)
	burstEnd := burstStart.Add(min(cfg.burstDuration, cfg.duration/3))
	var producers sync.WaitGroup
	for lane, rate := range []float64{float64(cfg.events), float64(cfg.sends) * .8, float64(cfg.sends) * .2, 10, .1} {
		producers.Go(func() {
			var sequence int64
			// Advance the planned timeline, including every missed slot. Rate changes
			// depend on the slot's deadline, never on how late the generator wakes.
			for next := start; next.Before(end); {
				if runCtx.Err() != nil {
					return
				}
				burst := !next.Before(burstStart) && next.Before(burstEnd)
				actualRate := rate
				if burst && lane < 3 {
					actualRate *= 3
				}
				interval := time.Duration(float64(time.Second) / actualRate)
				sequence++
				if delay := time.Until(next); delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-runCtx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
				counter := &counters[lane]
				if time.Since(next) >= interval {
					counter.missed.Add(1)
					next = next.Add(interval)
					continue
				}
				healthy := !next.Before(warmEnd) && !burst
				counter.offered.Add(1)
				if healthy {
					counter.healthyOffered.Add(1)
				}
				account := int((sequence + int64(cfg.seed)) % int64(cfg.accounts))
				shard := account % len(queues)
				select {
				case queues[shard] <- job{lane, sequence, healthy, next}:
				default:
					counter.rejected.Add(1)
				}
				next = next.Add(interval)
			}
		})
	}
	productionDone := make(chan struct{})
	go func() {
		producers.Wait()
		for _, q := range queues {
			close(q)
		}
		workers.Wait()
		close(productionDone)
	}()
	// Cancel and join producers/workers before service/storage/temp cleanup, even
	// when a resource assertion calls Fatal during production.
	defer func() { cancelRun(); <-productionDone }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var baseline, final runtime.MemStats
	var maxRSS, maxDisk int64
	var diskAtWarm int64
	var initialFD, initialGoroutines int
	sampledBaseline := false
	progressAt := start.Add(time.Minute)
	sample := func() {
		disk, e := capacityDiskUsage(root)
		if e != nil {
			recordError(e)
			bad.Add(1)
		}
		maxDisk = max(maxDisk, disk)
		rss := capacityRSS()
		maxRSS = max(maxRSS, rss)
		if disk > cfg.maxDisk {
			t.Errorf("disk budget exceeded: %d > %d; retained data was not deleted", disk, cfg.maxDisk)
		}
		if !sampledBaseline && !time.Now().Before(warmEnd) {
			runtime.GC()
			runtime.ReadMemStats(&baseline)
			initialGoroutines = runtime.NumGoroutine()
			initialFD = capacityFD()
			diskAtWarm = disk
			sampledBaseline = true
		}
		if time.Now().After(progressAt) {
			report, _ := json.Marshal(map[string]any{"progress": true, "elapsed_seconds": time.Since(start).Seconds(), "accounts": cfg.accounts, "completed_events": completed.Load(), "disk_bytes": disk, "rss_bytes": rss})
			t.Log(string(report))
			progressAt = time.Now().Add(time.Minute)
		}
	}
	running := true
	for running {
		select {
		case <-productionDone:
			running = false
		case <-ticker.C:
			sample()
			if t.Failed() {
				t.Fatal("capacity resource budget failed")
			}
		}
	}
	sample()
	drainStart := time.Now()
	var stats map[string]int64
	for {
		stats, err = st.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"webhook_pending", "outbox_queued", "outbox_sending", "schedules_active", "webhook_failed", "webhook_paused", "webhook_cancelled", "outbox_unknown", "outbox_failed"} {
			if _, ok := stats[key]; !ok {
				t.Fatalf("required capacity statistic missing: %s", key)
			}
		}
		if completed.Load() == counters[0].accepted.Load() && stats["webhook_pending"] == 0 && stats["outbox_queued"] == 0 && stats["outbox_sending"] == 0 && stats["schedules_active"] == 0 {
			break
		}
		if time.Since(drainStart) > 5*time.Minute {
			t.Error("backlog did not drain in five minutes")
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var sendCount, receiptCount int64
	if err = receiver.QueryRow(`SELECT COUNT(*) FROM sends`).Scan(&sendCount); err != nil {
		t.Fatal(err)
	}
	if err = receiver.QueryRow(`SELECT COUNT(*) FROM receipts WHERE target='fast'`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if sendCount != counters[1].accepted.Load()+counters[2].accepted.Load() {
		t.Errorf("provider sends=%d admitted immediate+scheduled=%d", sendCount, counters[1].accepted.Load()+counters[2].accepted.Load())
	}
	if receiptCount != counters[0].accepted.Load() || bad.Load() != 0 {
		t.Errorf("durable event delivery mismatch receipts=%d admitted=%d bad=%d", receiptCount, counters[0].accepted.Load(), bad.Load())
	}
	for _, state := range []string{"webhook_failed", "webhook_paused", "webhook_cancelled", "outbox_unknown", "outbox_failed"} {
		if stats[state] != 0 {
			t.Errorf("unexpected %s=%d", state, stats[state])
		}
	}
	for i := range counters {
		c := &counters[i]
		if c.healthyOffered.Load() > 0 && float64(c.healthyAccepted.Load())/float64(c.healthyOffered.Load()) < .95 {
			t.Errorf("lane %d admitted less than 95%% of healthy offered load", i)
		}
		if offered := c.offered.Load(); offered > 0 && float64(offered)/float64(offered+c.missed.Load()) < .95 {
			t.Errorf("lane %d generator missed >5%% target", i)
		}
	}
	if admissionLatency.percentile(.95) > 100 || admissionLatency.percentile(.99) > 500 {
		t.Error("durable admission latency target missed")
	}
	if fastLatency.percentile(.95) > 1000 || fastLatency.percentile(.99) > 5000 {
		t.Error("healthy webhook latency target missed")
	}
	runtime.GC()
	runtime.ReadMemStats(&final)
	if maxRSS >= 6<<30 {
		t.Error("RSS target exceeded")
	}
	if sampledBaseline && int64(final.HeapAlloc)-int64(baseline.HeapAlloc) > max(int64(baseline.HeapAlloc)*15/100, 64<<20) {
		t.Error("post-GC heap growth target exceeded")
	}
	finalGoroutines, finalFD := runtime.NumGoroutine(), capacityFD()
	if sampledBaseline && finalGoroutines-initialGoroutines > max(10, initialGoroutines/20) {
		t.Error("goroutine drift target exceeded")
	}
	fdSupported := initialFD >= 0 && finalFD >= 0
	if sampledBaseline && fdSupported && finalFD-initialFD > max(10, initialFD/20) {
		t.Error("file descriptor drift target exceeded")
	}
	report := map[string]any{"fd_measurement_supported": fdSupported, "measurement_scope": "synthetic provider, real REST/storage/webhook; process includes bounded harness", "accounts": cfg.accounts, "seed": cfg.seed, "duration_seconds": cfg.duration.Seconds(), "warmup_seconds": cfg.warmup.Seconds(), "elapsed_seconds": time.Since(start).Seconds(), "drain_seconds": time.Since(drainStart).Seconds(), "event_rate": cfg.events, "send_rate": cfg.sends, "workers": map[string]int{"send": 4, "webhook": 8, "reconnect": 4, "media": 4}, "poll_ms": 500, "queue_limit": capacityQueueLimit, "connection_queue_limit": capacityConnectionQueueLimit, "offered": capacityCounts(counters, "offered"), "admitted": capacityCounts(counters, "accepted"), "rejected": capacityCounts(counters, "rejected"), "missed": capacityCounts(counters, "missed"), "completed_events": receiptCount, "completed_sends": sendCount, "duplicate_deliveries": duplicates.Load(), "p95_persistence_ms": admissionLatency.percentile(.95), "p99_persistence_ms": admissionLatency.percentile(.99), "p95_healthy_webhook_ms": fastLatency.percentile(.95), "p99_healthy_webhook_ms": fastLatency.percentile(.99), "max_rss_bytes": maxRSS, "disk_bytes": maxDisk, "measured_disk_growth_bytes": max(0, maxDisk-diskAtWarm), "initial_heap_bytes": baseline.HeapAlloc, "final_heap_bytes": final.HeapAlloc, "initial_goroutines": initialGoroutines, "final_goroutines": finalGoroutines, "initial_fd": initialFD, "final_fd": finalFD, "passed": !t.Failed()}
	if e := firstError.Load(); e != nil {
		report["first_synthetic_error"] = *e
	}
	data, _ := json.Marshal(report)
	t.Log(string(data))
	if path := os.Getenv("GOBALE_SOAK_RESULT"); path != "" {
		if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Error(err)
		}
	}
}

type capacityClient struct {
	testClient
	account string
	sink    domains.Sink
	sendFn  func(domains.SendRequest) (domains.SendResult, error)
}

func (c *capacityClient) Connect(_ context.Context, _ *domains.Session, sink domains.Sink) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink = sink
	c.status = domains.ConnectionStatus{Auth: "authenticated", Transport: "connected", Recovery: "degraded"}
	return nil
}
func (c *capacityClient) emit(ctx context.Context, event domains.Event) error {
	c.mu.Lock()
	sink := c.sink
	connected := c.status.Transport == "connected"
	c.mu.Unlock()
	if !connected || sink == nil {
		return fmt.Errorf("synthetic client disconnected")
	}
	return sink(ctx, event)
}
func (c *capacityClient) Send(_ context.Context, r domains.SendRequest) (domains.SendResult, error) {
	return c.sendFn(r)
}

type capacityCounter struct{ offered, accepted, rejected, missed, healthyOffered, healthyAccepted atomic.Int64 }

func capacityCounts(c []capacityCounter, field string) map[string]int64 {
	out := map[string]int64{}
	for i, name := range []string{"event", "immediate", "schedule", "read", "media"} {
		v := int64(0)
		switch field {
		case "offered":
			v = c[i].offered.Load()
		case "accepted":
			v = c[i].accepted.Load()
		case "rejected":
			v = c[i].rejected.Load()
		case "missed":
			v = c[i].missed.Load()
		}
		out[name] = v
	}
	return out
}

var capacityBounds = []float64{1, 5, 10, 20, 50, 100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000, 1e9}

type capacityHistogram struct {
	mu      sync.Mutex
	buckets [15]int64
	count   int64
}

func (h *capacityHistogram) observe(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ms := float64(d) / float64(time.Millisecond)
	for i, b := range capacityBounds {
		if ms <= b {
			h.buckets[i]++
			break
		}
	}
	h.count++
}
func (h *capacityHistogram) percentile(f float64) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	target := int64(math.Ceil(float64(h.count) * f))
	var total int64
	for i, n := range h.buckets {
		total += n
		if total >= target {
			return capacityBounds[i]
		}
	}
	return 1e9
}

// Fixed production admission profile, recorded in every mixed result and runner manifest.
const (
	capacityQueueLimit           = 1000
	capacityConnectionQueueLimit = 100
)

type capacityConfig struct {
	accounts, events, sends, seed   int
	duration, warmup, burstDuration time.Duration
	maxDisk                         int64
}

func readCapacityConfig(t *testing.T) capacityConfig {
	t.Helper()
	integer := func(key string, fallback, min, max int) int {
		raw := os.Getenv(key)
		if raw == "" {
			return fallback
		}
		n, e := strconv.Atoi(raw)
		if e != nil || n < min || n > max {
			t.Fatalf("invalid %s", key)
		}
		return n
	}
	duration := func(key string, fallback time.Duration) time.Duration {
		raw := os.Getenv(key)
		if raw == "" {
			return fallback
		}
		d, e := time.ParseDuration(raw)
		if e != nil || d < 0 || d > 48*time.Hour {
			t.Fatalf("invalid %s", key)
		}
		return d
	}
	cfg := capacityConfig{accounts: integer("GOBALE_SOAK_ACCOUNTS", 300, 1, 1000), events: integer("GOBALE_SOAK_RATE", 60, 1, 10000), sends: integer("GOBALE_SOAK_SEND_RATE", 10, 5, 1000), seed: integer("GOBALE_SOAK_SEED", 1, 0, 1000000), duration: duration("GOBALE_SOAK_DURATION", time.Hour), warmup: duration("GOBALE_SOAK_WARMUP", 10*time.Minute), burstDuration: duration("GOBALE_SOAK_BURST_DURATION", time.Minute), maxDisk: int64(integer("GOBALE_SOAK_MAX_DISK_BYTES", 16<<30, 1<<20, 1<<40))}
	if cfg.duration < time.Second {
		t.Fatal("duration must be at least one second")
	}
	return cfg
}
func capacityDiskUsage(root string) (int64, error) {
	var n int64
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			s, err := e.Info()
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			n += s.Size()
		}
		return nil
	})
	return n, err
}
func capacityRSS() int64 {
	var r unix.Rusage
	if unix.Getrusage(unix.RUSAGE_SELF, &r) != nil {
		return 0
	}
	n := int64(r.Maxrss)
	if runtime.GOOS != "darwin" {
		n *= 1024
	}
	return n
}
func capacityFD() int {
	root := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		root = "/dev/fd"
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		return -1
	}
	return len(entries)
}
