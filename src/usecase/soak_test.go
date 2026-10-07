package usecase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/mimalef70/gobale/src/pkg/sqlite"
)

// TestOptionalSoak is deliberately opt-in: the ordinary suite never starts a
// 24-hour job. Everything runs against fake providers and a loopback webhook.
// Example: GOBALE_SOAK_DURATION=24h go test ./usecase -run TestOptionalSoak -count=1 -timeout 25h -v
// Smoke: GOBALE_SOAK_DURATION=10s go test ./usecase -run TestOptionalSoak -count=1 -timeout 1m -v
// The histogram reports measured upper bounds, not invented exact percentiles.
func TestOptionalSoak(t *testing.T) {
	raw := os.Getenv("GOBALE_SOAK_DURATION")
	if raw == "" {
		t.Skip("set GOBALE_SOAK_DURATION to enable the synthetic account soak")
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < time.Second || duration > 48*time.Hour {
		t.Fatal("GOBALE_SOAK_DURATION must be 1s..48h")
	}
	integer := func(name string, fallback int) int {
		value := os.Getenv(name)
		if value == "" {
			return fallback
		}
		n, e := strconv.Atoi(value)
		if e != nil || n < 1 || n > 10000 {
			t.Fatalf("%s must be 1..10000", name)
		}
		return n
	}
	accounts := integer("GOBALE_SOAK_ACCOUNTS", 50)
	if accounts > 1000 {
		t.Fatal("GOBALE_SOAK_ACCOUNTS must be 1..1000")
	}
	steadyRate := integer("GOBALE_SOAK_RATE", 20)
	burstRate := integer("GOBALE_SOAK_BURST_RATE", 100)
	burstDuration := 60 * time.Second
	if value := os.Getenv("GOBALE_SOAK_BURST_DURATION"); value != "" {
		burstDuration, err = time.ParseDuration(value)
		if err != nil || burstDuration <= 0 {
			t.Fatal("invalid GOBALE_SOAK_BURST_DURATION")
		}
	}
	ctx := context.Background()
	maxDiskBytes := int64(8 << 30)
	if value := os.Getenv("GOBALE_SOAK_MAX_DISK_BYTES"); value != "" {
		maxDiskBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || maxDiskBytes < 1<<20 || maxDiskBytes > 64<<30 {
			t.Fatal("GOBALE_SOAK_MAX_DISK_BYTES must be 1 MiB..64 GiB")
		}
	}
	key := []byte(strings.Repeat("z", 32))
	dbPath := filepath.Join(t.TempDir(), "soak.db")
	inboxPath := filepath.Join(t.TempDir(), "receiver.db")
	// The receiver retains deduplication and injected-failure history on disk.
	// No Go collection grows with every event during a 24-hour run.
	inbox, err := sql.Open(sqlite.DriverName, sqlite.FormatChatStorageURI((&url.URL{Scheme: "file", Path: inboxPath}).String(), true, true))
	if err != nil {
		t.Fatal(err)
	}
	inbox.SetMaxOpenConns(1)
	inbox.SetMaxIdleConns(1)
	defer inbox.Close()
	for _, query := range []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `PRAGMA busy_timeout=5000`,
		`PRAGMA cache_size=-2048`, `PRAGMA mmap_size=0`, `PRAGMA temp_store=FILE`,
		`CREATE TABLE inbox(event_id TEXT PRIMARY KEY,sequence INTEGER NOT NULL UNIQUE,account INTEGER NOT NULL,acknowledged INTEGER NOT NULL DEFAULT 0,failed_once INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX inbox_account_ack ON inbox(account,acknowledged)`,
	} {
		if _, err = inbox.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	boundaries := []float64{1, 5, 10, 20, 50, 100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000}
	histogram := make([]int64, len(boundaries)+1)
	persistHistogram := make([]int64, len(boundaries)+1)
	steadyHealthyHistogram := make([]int64, len(boundaries)+1)
	var steadyHealthyAcks int64
	record := func(hist []int64, elapsed float64) {
		bucket := len(boundaries)
		for i, bound := range boundaries {
			if elapsed <= bound {
				bucket = i
				break
			}
		}
		hist[bucket]++
	}
	var receiveMu sync.Mutex
	unique := int64(0)
	duplicateAcks := int64(0)
	httpAttempts := int64(0)
	var failures atomic.Int64
	var receiverError error
	accountAcks := make([]int64, accounts)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, 128<<10))
		if e != nil {
			failures.Add(1)
			w.WriteHeader(400)
			return
		}
		mac := hmac.New(sha256.New, []byte("soak-webhook-secret"))
		mac.Write(body)
		if r.Header.Get("X-Hub-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			failures.Add(1)
			w.WriteHeader(401)
			return
		}
		var event domains.Event
		var payload struct {
			Sequence int   `json:"sequence"`
			Account  int   `json:"account"`
			Created  int64 `json:"created_ns"`
			Steady   bool  `json:"steady"`
		}
		if json.Unmarshal(body, &event) != nil || json.Unmarshal(event.Payload, &payload) != nil || event.ID == "" || payload.Sequence < 1 || payload.Account < 0 || payload.Account >= accounts || event.SessionID != fmt.Sprint("soak-", payload.Account) || event.AccountID != strconv.Itoa(payload.Account+1) {
			failures.Add(1)
			w.WriteHeader(400)
			return
		}
		// Every twentieth event is deliberately slow. Every 101st event fails its
		// first HTTP attempt; the prime interval distributes faults across the
		// 50 accounts instead of concentrating every failure on the same account.
		receiveMu.Lock()
		httpAttempts++
		receiveMu.Unlock()
		if payload.Sequence%20 == 0 {
			time.Sleep(150 * time.Millisecond)
		}
		// A receiver may commit despite the sender cancelling an in-flight HTTP
		// request. This deliberately preserves at-least-once restart behavior.
		inboxCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		newAck, injectedFailure, e := soakReceive(inboxCtx, inbox, event.ID, payload.Sequence, payload.Account)
		if e != nil {
			failures.Add(1)
			receiveMu.Lock()
			if receiverError == nil {
				receiverError = e
			}
			receiveMu.Unlock()
			w.WriteHeader(500)
			return
		}
		if injectedFailure {
			w.WriteHeader(503)
			return
		}
		elapsed := float64(time.Since(time.Unix(0, payload.Created)).Microseconds()) / 1000
		receiveMu.Lock()
		if !newAck {
			duplicateAcks++
		} else {
			unique++
			accountAcks[payload.Account]++
			record(histogram, elapsed)
			if payload.Steady && payload.Sequence%20 != 0 && payload.Sequence%101 != 0 {
				record(steadyHealthyHistogram, elapsed)
				steadyHealthyAcks++
			}
		}
		receiveMu.Unlock()
		w.WriteHeader(204)
	}))
	defer server.Close()
	var service *Service
	var store *storage.Store
	var clientsMu sync.Mutex
	clients := map[string]*fakeClient{}
	var connects atomic.Int64
	factory := func(d domains.Device) domains.Client {
		f := &fakeClient{status: domains.ConnectionStatus{Auth: "unauthenticated", Transport: "disconnected", Recovery: "degraded"}}
		f.connectFn = func(context.Context, *domains.Session, domains.Sink) error {
			connects.Add(1)
			f.mu.Lock()
			f.status = readyStatus()
			f.mu.Unlock()
			return nil
		}
		clientsMu.Lock()
		clients[d.ID] = f
		clientsMu.Unlock()
		return f
	}
	boot := func() {
		var e error
		store, e = storage.Open(dbPath, key)
		if e != nil {
			t.Fatal(e)
		}
		service = New(store, Options{PollInterval: 500 * time.Millisecond, GlobalWebhooks: []storage.WebhookTarget{{URL: server.URL, Secret: "soak-webhook-secret"}}}, factory)
		if e = service.Start(ctx); e != nil {
			t.Fatal(e)
		}
	}
	store, err = storage.Open(dbPath, key)
	if err != nil {
		t.Fatal(err)
	}
	devices := make([]domains.Device, accounts)
	for i := range devices {
		devices[i], err = store.CreateDevice(ctx, fmt.Sprint("soak-", i))
		if err != nil {
			t.Fatal(err)
		}
		if err = store.SaveSession(ctx, devices[i].ConnectionID, &domains.Session{UserID: strconv.Itoa(i + 1), Token: "synthetic-session"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	boot()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if service != nil {
			if e := service.Close(stopCtx); e != nil {
				t.Error(e)
			}
		}
		if store != nil {
			if e := store.Close(); e != nil {
				t.Error(e)
			}
		}
	})
	start := time.Now()
	runtime.GC()
	var initialMemory runtime.MemStats
	runtime.ReadMemStats(&initialMemory)
	initialGoroutines := runtime.NumGoroutine()
	end := start.Add(duration)
	burstStart := start.Add(min(time.Minute, duration/3))
	burstEnd := burstStart.Add(burstDuration)
	restartInterval := min(15*time.Minute, duration/2)
	nextRestart := start.Add(restartInterval)
	disconnectInterval := min(time.Minute, duration/4)
	nextDisconnect := start.Add(disconnectInterval)
	accepted := 0
	offered := 0
	skipped := 0
	accountAccepted := make([]int64, accounts)
	restarts := 0
	disconnects := 0
	var maxHeap uint64
	var maxDiskUsed int64
	maxGoroutines := 0
	nextSample := start
	// Duration is capped at 48 hours, so this emits at most 192 compact records.
	// Keep trend evidence without accumulating an event-sized in-memory trace.
	nextProgress := start.Add(15 * time.Minute)
	deadline := start
	for time.Now().Before(end) {
		now := time.Now()
		if !now.Before(nextSample) {
			diskBytes, err := soakDiskUsage(dbPath, inboxPath)
			if err != nil {
				t.Fatal(err)
			}
			if diskBytes > maxDiskUsed {
				maxDiskUsed = diskBytes
			}
			if diskBytes > maxDiskBytes {
				t.Fatalf("synthetic disk cap exceeded: used=%d cap=%d accepted=%d; run stopped without deleting retained work", diskBytes, maxDiskBytes, accepted)
			}
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			if memory.HeapAlloc > maxHeap {
				maxHeap = memory.HeapAlloc
			}
			if n := runtime.NumGoroutine(); n > maxGoroutines {
				maxGoroutines = n
			}
			nextSample = now.Add(time.Second)
		}
		if !now.Before(nextProgress) {
			runtime.GC()
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			receiveMu.Lock()
			acknowledged := unique
			receiveMu.Unlock()
			stats, err := store.Stats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			progress, _ := json.Marshal(map[string]any{"synthetic": true, "progress": true, "elapsed_seconds": time.Since(start).Seconds(), "accepted_events": accepted, "unique_acknowledgements": acknowledged, "process_heap_after_gc_bytes": memory.HeapAlloc, "process_goroutines": runtime.NumGoroutine(), "sampled_disk_max_bytes": maxDiskUsed, "disk_cap_bytes": maxDiskBytes, "webhook_pending": stats["webhook_pending"], "webhook_failed": stats["webhook_failed"], "webhook_paused": stats["webhook_paused"], "webhook_cancelled": stats["webhook_cancelled"]})
			t.Log(string(progress))
			nextProgress = now.Add(15 * time.Minute)
		}
		if !now.Before(nextRestart) && nextRestart.Before(end) {
			stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			e := service.Close(stopCtx)
			cancel()
			if e != nil {
				t.Fatal(e)
			}
			if e = store.Close(); e != nil {
				t.Fatal(e)
			}
			store = nil
			boot()
			restarts++
			nextRestart = now.Add(restartInterval)
		}
		if !now.Before(nextDisconnect) {
			index := disconnects % accounts
			clientsMu.Lock()
			client := clients[devices[index].ID]
			clientsMu.Unlock()
			if client != nil {
				client.mu.Lock()
				client.status.Transport = "disconnected"
				client.mu.Unlock()
				disconnects++
			}
			nextDisconnect = now.Add(disconnectInterval)
		}
		rate := steadyRate
		if !now.Before(burstStart) && now.Before(burstEnd) {
			rate = burstRate
		}
		deadline = deadline.Add(time.Second / time.Duration(rate))
		if deadline.Before(now.Add(-time.Second)) {
			deadline = now
		}
		if wait := time.Until(deadline); wait > 0 {
			time.Sleep(wait)
		}
		index := offered % accounts
		offered++
		clientsMu.Lock()
		client := clients[devices[index].ID]
		clientsMu.Unlock()
		if client == nil || client.Status().Transport != "connected" {
			skipped++
			continue
		}
		sequence := accepted + 1
		payload, _ := json.Marshal(map[string]any{"sequence": sequence, "account": index, "created_ns": time.Now().UnixNano(), "steady": rate == steadyRate})
		event := domains.Event{ID: fmt.Sprint("soak-event-", sequence), Type: "message", Peer: domains.Peer{Type: "user", ID: "123"}, MessageID: strconv.Itoa(sequence), AccountID: strconv.Itoa(index + 1), Time: time.Now().UTC(), Payload: payload}
		persistStart := time.Now()
		if e := service.sink(devices[index].ConnectionID)(ctx, event); e != nil {
			t.Fatal(e)
		}
		record(persistHistogram, float64(time.Since(persistStart).Microseconds())/1000)
		accepted++
		accountAccepted[index]++
	}
	drainDeadline := time.Now().Add(5 * time.Minute)
	var finalStats map[string]int64
	for time.Now().Before(drainDeadline) {
		receiveMu.Lock()
		done := unique == int64(accepted)
		receiveMu.Unlock()
		finalStats, err = store.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if done && finalStats["webhook_pending"] == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	receiveMu.Lock()
	defer receiveMu.Unlock()
	var durableUnique int64
	if err = inbox.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox WHERE acknowledged=1`).Scan(&durableUnique); err != nil {
		t.Fatal(err)
	}
	percentile := func(hist []int64, total int64, fraction float64) any {
		if total == 0 {
			return nil
		}
		target := int64(float64(total)*fraction + .999)
		var cumulative int64
		for i, count := range hist {
			cumulative += count
			if cumulative >= target {
				if i == len(boundaries) {
					return ">60000"
				}
				return boundaries[i]
			}
		}
		return nil
	}
	runtime.GC()
	var finalMemory runtime.MemStats
	runtime.ReadMemStats(&finalMemory)
	report := map[string]any{"synthetic": true, "duration_seconds": duration.Seconds(), "elapsed_with_drain_seconds": time.Since(start).Seconds(), "accounts": accounts, "offered_events": offered, "skipped_disconnected_events": skipped, "steady_target_events_per_second": steadyRate, "burst_target_events_per_second": burstRate, "configured_burst_seconds": burstDuration.Seconds(), "observed_burst_window_seconds": max(0, min(duration-burstStart.Sub(start), burstDuration).Seconds()), "accepted_events": accepted, "unique_acknowledgements": unique, "http_attempts": httpAttempts, "duplicate_acknowledgements": duplicateAcks, "restarts": restarts, "fake_disconnects": disconnects, "connect_calls": connects.Load(), "p50_ack_latency_upper_bound_ms": percentile(histogram, unique, .5), "p95_ack_latency_upper_bound_ms": percentile(histogram, unique, .95), "p99_ack_latency_upper_bound_ms": percentile(histogram, unique, .99), "p95_persistence_latency_upper_bound_ms": percentile(persistHistogram, int64(accepted), .95), "steady_healthy_acknowledgements": steadyHealthyAcks, "p95_steady_healthy_ack_latency_upper_bound_ms": percentile(steadyHealthyHistogram, steadyHealthyAcks, .95), "sampled_process_heap_max_bytes": maxHeap, "initial_process_heap_after_gc_bytes": initialMemory.HeapAlloc, "post_drain_process_heap_after_gc_bytes": finalMemory.HeapAlloc, "sampled_process_goroutines_max": maxGoroutines, "initial_process_goroutines": initialGoroutines, "post_drain_process_goroutines": runtime.NumGoroutine(), "final_webhook_pending": finalStats["webhook_pending"], "final_webhook_failed": finalStats["webhook_failed"], "measurement_scope": "fake provider, loopback HTTP, process measurements include test harness; not live capacity"}
	report["receiver_dedupe"] = "SQLite WAL, 2 MiB page cache, fixed-size in-memory counters"
	report["receiver_durable_unique_acknowledgements"] = durableUnique
	report["final_webhook_paused"] = finalStats["webhook_paused"]
	report["final_webhook_cancelled"] = finalStats["webhook_cancelled"]
	report["final_webhook_delivered"] = finalStats["webhook_delivered"]
	report["sampled_disk_max_bytes"] = maxDiskUsed
	report["disk_cap_bytes"] = maxDiskBytes
	encoded, _ := json.Marshal(report)
	t.Log(string(encoded))
	if failures.Load() != 0 || unique != int64(accepted) || durableUnique != unique || finalStats["webhook_pending"] != 0 || finalStats["webhook_failed"] != 0 || finalStats["webhook_paused"] != 0 || finalStats["webhook_cancelled"] != 0 || finalStats["webhook_delivered"] != int64(accepted) {
		t.Fatalf("soak routing/drain: accepted=%d acknowledged=%d durable_acknowledged=%d bad=%d states=%v receiver_error=%v", accepted, unique, durableUnique, failures.Load(), finalStats, receiverError)
	}
	for account, expected := range accountAccepted {
		if accountAcks[account] != expected {
			t.Fatalf("account %d accepted=%d acknowledged=%d", account, expected, accountAcks[account])
		}
		var durableAccount int64
		if err = inbox.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox WHERE acknowledged=1 AND account=?`, account).Scan(&durableAccount); err != nil {
			t.Fatal(err)
		}
		if durableAccount != expected {
			t.Fatalf("account %d accepted=%d durable_acknowledged=%d", account, expected, durableAccount)
		}
	}
}

func soakDiskUsage(paths ...string) (int64, error) {
	var total int64
	for _, path := range paths {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			info, err := os.Stat(path + suffix)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return 0, err
			}
			total += info.Size()
		}
	}
	return total, nil
}

func TestSoakDiskUsageIncludesJournalsAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	for _, fixture := range []struct{ name, body string }{{"one.db", "123"}, {"one.db-wal", "12345"}, {"one.db-shm", "1234567"}, {"two.db", "1"}} {
		if err := os.WriteFile(filepath.Join(dir, fixture.name), []byte(fixture.body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := soakDiskUsage(filepath.Join(dir, "one.db"), filepath.Join(dir, "two.db"), filepath.Join(dir, "missing.db"))
	if err != nil || got != 16 {
		t.Fatalf("disk accounting: bytes=%d err=%v", got, err)
	}
}

// soakReceive atomically deduplicates event identity, validates its sequence and
// account, and commits a one-time injected failure or acknowledgement. The
// receiver database contains only synthetic identifiers and integer counters.
func soakReceive(ctx context.Context, db *sql.DB, eventID string, sequence, account int) (newAck, injectedFailure bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO inbox(event_id,sequence,account) VALUES(?,?,?) ON CONFLICT(event_id) DO NOTHING`, eventID, sequence, account); err != nil {
		return false, false, err
	}
	var storedSequence, storedAccount, acknowledged, failedOnce int
	if err = tx.QueryRowContext(ctx, `SELECT sequence,account,acknowledged,failed_once FROM inbox WHERE event_id=?`, eventID).Scan(&storedSequence, &storedAccount, &acknowledged, &failedOnce); err != nil {
		return false, false, err
	}
	if storedSequence != sequence || storedAccount != account {
		return false, false, fmt.Errorf("receiver event identity conflicts with sequence/account")
	}
	if sequence%101 == 0 && acknowledged == 0 && failedOnce == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE inbox SET failed_once=1 WHERE event_id=?`, eventID); err != nil {
			return false, false, err
		}
		return false, true, tx.Commit()
	}
	if acknowledged == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE inbox SET acknowledged=1 WHERE event_id=?`, eventID); err != nil {
			return false, false, err
		}
	}
	return acknowledged == 0, false, tx.Commit()
}
