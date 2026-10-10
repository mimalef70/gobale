package usecase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	domainSend "github.com/mimalef70/goomni/src/domains/send"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
)

type fakeClient struct {
	mu          sync.Mutex
	status      domains.ConnectionStatus
	authSession *domains.Session
	sendFn      func(context.Context, domains.SendRequest) (domains.SendResult, error)
	connectFn   func(context.Context, *domains.Session, domains.Sink) error
	sendCalls   int
	logoutErr   error
	callErr     error
	callFn      func(context.Context, string, json.RawMessage) (json.RawMessage, error)
	callCalls   int
	sink        domains.Sink
}

func (f *fakeClient) StartAuth(context.Context, string) (domains.Challenge, error) {
	return domains.Challenge{ID: "challenge", State: "awaiting_code"}, nil
}
func (f *fakeClient) SubmitCode(context.Context, string, string) (*domains.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authSession, nil
}
func (f *fakeClient) SubmitPassword(ctx context.Context, c, p string) (*domains.Session, error) {
	return f.SubmitCode(ctx, c, p)
}
func (f *fakeClient) Connect(ctx context.Context, s *domains.Session, sink domains.Sink) error {
	f.mu.Lock()
	fn := f.connectFn
	f.sink = sink
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, s, sink)
	}
	f.mu.Lock()
	f.status = readyStatus()
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) Disconnect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.Transport = "disconnected"
	return nil
}
func (f *fakeClient) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.Auth = "logged_out"
	return f.logoutErr
}
func (f *fakeClient) Status() domains.ConnectionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}
func (f *fakeClient) Send(ctx context.Context, r domains.SendRequest) (domains.SendResult, error) {
	f.mu.Lock()
	f.sendCalls++
	fn := f.sendFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, r)
	}
	return domains.SendResult{MessageID: r.RequestID, Date: time.Now()}, nil
}
func (f *fakeClient) Call(ctx context.Context, operation string, payload json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	f.callCalls++
	fn, err := f.callFn, f.callErr
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, operation, payload)
	}
	return json.RawMessage(`{}`), err
}

func readyStatus() domains.ConnectionStatus {
	return domains.ConnectionStatus{Auth: "authenticated", Transport: "connected", Recovery: "degraded"}
}
func testService(t *testing.T, options Options, factory domains.ClientFactory) (*Service, *storage.Store) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "test.db"), []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, options, factory)
	s.ctx = context.Background()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, st
}
func mustDevice(t *testing.T, s *Service, id string) domains.Device {
	t.Helper()
	d, e := s.CreateDevice(context.Background(), id, domains.ProviderBale)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func sendReq(text string) domains.SendRequest {
	return domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "12"}, Text: text}
}
func codeIs(t *testing.T, err error, code string) {
	t.Helper()
	var e *domains.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}
func claimOne(t *testing.T, st *storage.Store) domains.Operation {
	t.Helper()
	jobs, e := st.ClaimOperations(context.Background(), 1)
	if e != nil || len(jobs) != 1 {
		t.Fatalf("claim: %v %v", jobs, e)
	}
	return jobs[0]
}

func TestOutboxPersistsBeforeProviderAndIdempotency(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{status: readyStatus()}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "alpha")
	op, err := s.Send(ctx, d.ID, sendReq("hello"), "same")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Send(ctx, d.ID, sendReq("hello"), "same")
	if err != nil || again.ID != op.ID {
		t.Fatalf("idempotency: %v %v", again, err)
	}
	_, err = s.Send(ctx, d.ID, sendReq("different"), "same")
	codeIs(t, err, "IDEMPOTENCY_CONFLICT")
	f.sendFn = func(_ context.Context, r domains.SendRequest) (domains.SendResult, error) {
		saved, e := st.GetOperation(ctx, d.ConnectionID, op.ID)
		if e != nil || saved.State != "sending" || r.RequestID == "" || saved.Request.RequestID != r.RequestID {
			t.Errorf("provider called before durable request: %+v %v", saved, e)
		}
		return domains.SendResult{MessageID: r.RequestID}, nil
	}
	s.processOperation(claimOne(t, st))
	saved, err := s.GetOperation(ctx, d.ID, op.ID)
	if err != nil || saved.State != "succeeded" || f.sendCalls != 1 {
		t.Fatalf("result %+v %v calls=%d", saved, err, f.sendCalls)
	}
}
func TestAmbiguousSendIsNeverAutomaticallyRetried(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{status: readyStatus(), sendFn: func(context.Context, domains.SendRequest) (domains.SendResult, error) {
		return domains.SendResult{}, &domains.Error{Code: "SEND_UNKNOWN", Message: "token=secret", Ambiguous: true, HTTP: 504}
	}}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "alpha")
	op, e := s.Send(ctx, d.ID, sendReq("hello"), "key")
	if e != nil {
		t.Fatal(e)
	}
	s.processOperation(claimOne(t, st))
	saved, _ := s.GetOperation(ctx, d.ID, op.ID)
	if saved.State != "unknown" || strings.Contains(saved.ErrorMessage, "secret") {
		t.Fatalf("unsafe outcome %+v", saved)
	}
	jobs, e := st.ClaimOperations(ctx, 10)
	if e != nil || len(jobs) != 0 {
		t.Fatalf("unknown was retried %+v %v", jobs, e)
	}
	again, e := s.Send(ctx, d.ID, sendReq("hello"), "key")
	if e != nil || again.ID != op.ID || again.State != "unknown" {
		t.Fatalf("duplicate accepted %+v %v", again, e)
	}
}

func TestConfirmedOperationWinsOverLateAmbiguousRPCResult(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{status: readyStatus()}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "alpha")
	op, err := s.Send(ctx, d.ID, sendReq("confirmed by provider update"), "echo-race")
	if err != nil {
		t.Fatal(err)
	}
	f.sendFn = func(_ context.Context, r domains.SendRequest) (domains.SendResult, error) {
		// Simulate an independently persisted own-message proof winning the
		// race before the waiting RPC reports its ambiguous transport result.
		if err := st.FinishOperation(ctx, d.ConnectionID, op.ID, "succeeded", &domains.SendResult{MessageID: r.RequestID}, "", ""); err != nil {
			t.Fatal(err)
		}
		return domains.SendResult{}, &domains.Error{Code: "SEND_UNKNOWN", Ambiguous: true}
	}
	s.processOperation(claimOne(t, st))
	saved, err := s.GetOperation(ctx, d.ID, op.ID)
	if err != nil || saved.State != "succeeded" || saved.Result == nil || saved.Result.MessageID != saved.Request.RequestID {
		t.Fatalf("confirmed proof was overwritten: %+v %v", saved, err)
	}
	if metrics := s.WorkerMetrics(); metrics.WorkerErrors != 0 {
		t.Fatalf("expected proof race counted as worker failure: %+v", metrics)
	}
}

func TestWebhookRetryJitterHasBoundedNonzeroDelay(t *testing.T) {
	for _, attempt := range []int{0, 1, 2, 7, 8, 20} {
		capped := max(1, min(8, attempt))
		ceiling := time.Duration(1<<uint(capped)) * time.Second
		for i := 0; i < 100; i++ {
			if d := webhookRetryDelay(attempt); d < ceiling/2 || d > ceiling {
				t.Fatalf("attempt %d has out-of-bounds delay %s", attempt, d)
			}
		}
	}
}

func TestStatusPreservesOnlyReviewedDiagnosticCodes(t *testing.T) {
	for _, code := range []string{"", "CONNECT_FAILED", "EVENT_PERSIST_FAILED", "UPDATE_ACCEPTANCE_FAILED", "RECOVERY_GAP", "WS_CLOSE_1006", "CONNECTION_CLOSED_1006"} {
		if got := cleanStatus(domains.ConnectionStatus{LastError: code}); got.LastError != code {
			t.Fatalf("reviewed diagnostic was hidden: %q", code)
		}
	}
	for _, unsafe := range []string{"token=secret", "UNREVIEWED_SECRET_CODE", "WS_CLOSE_1006 token=secret", "WS_CLOSE_12345", "WS_CLOSE_abcd", "CONNECTION_CLOSED_1006 token=secret"} {
		if got := cleanStatus(domains.ConnectionStatus{LastError: unsafe}); got.LastError != "provider reported a connection error" {
			t.Fatalf("unreviewed diagnostic was exposed: %q", got.LastError)
		}
	}
}
func TestExplicitInvalidDeviceNeverFallsBack(t *testing.T) {
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	mustDevice(t, s, "only")
	d, e := s.ResolveDevice(context.Background(), "")
	if e != nil || d.ID != "only" {
		t.Fatal(d, e)
	}
	_, e = s.ResolveDevice(context.Background(), "missing")
	if e == nil {
		t.Fatal("explicit invalid selector fell back")
	}
}
func TestAccountIdentityCannotBeReboundAfterLogout(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{authSession: &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "100", Token: "secret-one"}}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "alpha")
	if _, e := s.StartAuth(ctx, d.ID, "+15550000100"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SubmitCode(ctx, d.ID, "challenge", "123"); e != nil {
		t.Fatal(e)
	}
	if e := s.Logout(ctx, d.ID); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.authSession = &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "200", Token: "secret-two"}
	f.mu.Unlock()
	if _, e := s.StartAuth(ctx, d.ID, "+15550000200"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SubmitCode(ctx, d.ID, "challenge", "456"); e == nil {
		t.Fatal("account rebound")
	}
	session, e := st.LoadSession(ctx, d.ConnectionID)
	if e != nil || session != nil {
		t.Fatalf("rebound session persisted: %+v %v", session, e)
	}
	saved, e := s.GetDevice(ctx, d.ID)
	if e != nil || saved.AccountID != "100" {
		t.Fatal(saved, e)
	}
}
func TestWebhookPersistDeduplicateSignAndRetry(t *testing.T) {
	var calls atomic.Int32
	var capturedID atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("webhook-secret"))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if r.Header.Get("X-Hub-Signature-256") != want {
			t.Errorf("bad signature")
		}
		if r.Header.Get("X-GoOmni-Event-Id") == "" {
			t.Error("missing event id")
		}
		if prior := capturedID.Load(); prior != nil && prior.(string) != r.Header.Get("X-GoOmni-Delivery-Id") {
			t.Error("retry changed delivery id")
		}
		capturedID.Store(r.Header.Get("X-GoOmni-Delivery-Id"))
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "alpha")
	secret := "webhook-secret"
	_, e := s.PatchWebhook(context.Background(), d.ID, domains.WebhookPatch{URL: &server.URL, Secret: &secret})
	if e != nil {
		t.Fatal(e)
	}
	ev := domains.Event{ID: "provider-event", Type: "message", Peer: domains.Peer{Type: "user", ID: "12"}, MessageID: "77", Payload: json.RawMessage(`{"message":"hello"}`)}
	if e = s.sink(d.ConnectionID)(context.Background(), ev); e != nil {
		t.Fatal(e)
	}
	if e = s.sink(d.ConnectionID)(context.Background(), ev); e != nil {
		t.Fatal(e)
	}
	deliveries, e := st.ListDeliveries(context.Background(), d.ConnectionID, 10, 0)
	if e != nil || len(deliveries) != 1 || calls.Load() != 0 {
		t.Fatalf("not durably deduplicated %+v %v", deliveries, e)
	}
	jobs, e := st.ClaimDeliveries(context.Background(), 1, time.Now().Add(time.Second))
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	s.deliver(jobs[0])
	after, _ := st.GetDelivery(context.Background(), d.ConnectionID, deliveries[0].ID)
	if after.State != "retry" || after.Attempts != 1 {
		t.Fatal(after)
	}
	jobs, e = st.ClaimDeliveries(context.Background(), 1, time.Now().Add(time.Minute))
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	s.deliver(jobs[0])
	after, _ = st.GetDelivery(context.Background(), d.ConnectionID, deliveries[0].ID)
	if after.State != "delivered" || after.Attempts != 2 || calls.Load() != 2 {
		t.Fatal(after, calls.Load())
	}
}
func TestURLChangePausesClaimedDeliveryUntilExplicitReplay(t *testing.T) {
	var oldCalls, newCalls atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { oldCalls.Add(1); w.WriteHeader(204) }))
	defer old.Close()
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { newCalls.Add(1); w.WriteHeader(204) }))
	defer replacement.Close()
	ctx := context.Background()
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "alpha")
	secret := "test-webhook-secret"
	_, _ = s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{URL: &old.URL, Secret: &secret})
	if e := s.sink(d.ConnectionID)(ctx, domains.Event{ID: "one", Type: "message", Payload: json.RawMessage(`{}`)}); e != nil {
		t.Fatal(e)
	}
	jobs, e := st.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	_, _ = s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{URL: &replacement.URL})
	s.deliver(jobs[0])
	paused, _ := st.GetDelivery(ctx, d.ConnectionID, jobs[0].ID)
	if paused.State != "paused" || oldCalls.Load() != 0 || newCalls.Load() != 0 {
		t.Fatal(paused)
	}
	replay, e := s.ReplayDelivery(ctx, d.ID, paused.ID)
	if e != nil || len(replay) != 1 {
		t.Fatal(replay, e)
	}
	jobs, e = st.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	s.deliver(jobs[0])
	if newCalls.Load() != 1 {
		t.Fatal("replay not delivered")
	}
}
func TestScheduleMaterializationIsAtomicAndOccurrenceBounded(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{status: readyStatus()} })
	d := mustDevice(t, s, "alpha")
	request := sendReq("scheduled")
	request.ScheduleOptions = domainSend.ScheduleOptions{ScheduledAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Timezone: "UTC", Recurrence: "daily", OccurrenceLimit: 1}
	job, e := s.CreateSchedule(ctx, d.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	s.materializeSchedule(job)
	s.materializeSchedule(job)
	saved, e := s.GetSchedule(ctx, d.ID, job.ID)
	if e != nil || saved.State != "completed" || saved.Count != 1 {
		t.Fatalf("bad schedule %+v %v", saved, e)
	}
	ops, e := st.ListOperations(ctx, d.ConnectionID, 100, 0)
	if e != nil || len(ops) != 1 {
		t.Fatalf("duplicate occurrence %+v %v", ops, e)
	}
}
func TestProviderErrorsNeverExposeSecrets(t *testing.T) {
	err := safeError(&domains.Error{Code: "AUTH_REJECTED", Message: "https://provider.invalid?token=super-secret", HTTP: 401})
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatal(err)
	}
	f := &fakeClient{status: readyStatus(), callErr: errors.New("raw-session-token")}
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "alpha")
	_, e := s.Call(context.Background(), d.ID, "banking.transfer", json.RawMessage(`{}`))
	codeIs(t, e, "FEATURE_NOT_SUPPORTED")
	_, e = s.Call(context.Background(), d.ID, "chat.list", json.RawMessage(`{}`))
	if strings.Contains(e.Error(), "raw-session-token") {
		t.Fatal(e)
	}
}
func TestWorkersSerializePerDeviceAndShutdownAmbiguousSend(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{}, 1)
	f := &fakeClient{status: readyStatus(), sendFn: func(ctx context.Context, r domains.SendRequest) (domains.SendResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return domains.SendResult{}, ctx.Err()
	}}
	s, st := testService(t, Options{PollInterval: time.Millisecond * 5, SendWorkers: 4}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "alpha")
	op, e := s.Send(ctx, d.ID, sendReq("hello"), "one")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Send(ctx, d.ID, sendReq("later"), "two"); e != nil {
		t.Fatal(e)
	}
	if e = s.Start(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("send worker did not start")
	}
	time.Sleep(30 * time.Millisecond)
	f.mu.Lock()
	calls := f.sendCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("same device sends overlapped: %d", calls)
	}
	statusDone := make(chan error, 1)
	go func() { _, err := s.Status(ctx, d.ID); statusDone <- err }()
	select {
	case err := <-statusDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("status blocked behind a provider send")
	}
	stopCtx, cancel := context.WithTimeout(ctx, time.Second*2)
	defer cancel()
	if e = s.Close(stopCtx); e != nil {
		t.Fatal(e)
	}
	saved, e := st.GetOperation(ctx, d.ConnectionID, op.ID)
	if e != nil || saved.State != "unknown" {
		t.Fatalf("shutdown lost ambiguous outcome %+v %v", saved, e)
	}
}

func TestGlobalWebhookTargetsDeliverAndDeviceOverrideIsIndependent(t *testing.T) {
	ctx := context.Background()
	var globalCalls, deviceCalls atomic.Int32
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("global-key"))
		mac.Write(body)
		if r.Header.Get("X-Hub-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("global secret replaced by device secret")
		}
		globalCalls.Add(1)
		w.WriteHeader(204)
	}))
	defer global.Close()
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { deviceCalls.Add(1); w.WriteHeader(204) }))
	defer device.Close()
	options := Options{MergeGlobal: true, GlobalWebhooks: []storage.WebhookTarget{{URL: global.URL, Secret: "global-key"}}}
	s, st := testService(t, options, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "alpha")
	secret := "device-key"
	if _, e := s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{URL: &device.URL, Secret: &secret}); e != nil {
		t.Fatal(e)
	}
	if e := s.sink(d.ConnectionID)(ctx, domains.Event{ID: "one", Type: "message", Payload: json.RawMessage(`{}`)}); e != nil {
		t.Fatal(e)
	}
	jobs, e := st.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	if e != nil || len(jobs) != 2 {
		t.Fatal(jobs, e)
	}
	for _, job := range jobs {
		s.deliver(job)
	}
	if globalCalls.Load() != 1 || deviceCalls.Load() != 1 {
		t.Fatalf("global=%d device=%d", globalCalls.Load(), deviceCalls.Load())
	}
}
func TestPersistedSessionsRestoreWithBoundedReconnectConcurrency(t *testing.T) {
	ctx := context.Background()
	var active, maxActive, started atomic.Int32
	release := make(chan struct{})
	var allMu sync.Mutex
	clients := map[string]*fakeClient{}
	s, st := testService(t, Options{PollInterval: time.Millisecond * 5, ReconnectWorkers: 4}, func(d domains.Device) domains.Client {
		f := &fakeClient{status: domains.ConnectionStatus{Auth: "unauthenticated", Transport: "disconnected", Recovery: "degraded"}}
		f.connectFn = func(ctx context.Context, _ *domains.Session, _ domains.Sink) error {
			n := active.Add(1)
			defer active.Add(-1)
			for {
				old := maxActive.Load()
				if n <= old || maxActive.CompareAndSwap(old, n) {
					break
				}
			}
			started.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			f.mu.Lock()
			f.status = readyStatus()
			f.mu.Unlock()
			return nil
		}
		allMu.Lock()
		clients[d.ID] = f
		allMu.Unlock()
		return f
	})
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		d := mustDevice(t, s, id)
		if e := st.SaveSession(ctx, d.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: strconv.Itoa(int(id[0])), Token: "test-token"}); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Start(ctx); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(2 * time.Second)
	for started.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond * 5)
	}
	if started.Load() != 4 || maxActive.Load() > 4 {
		close(release)
		t.Fatalf("reconnect concurrency started=%d max=%d", started.Load(), maxActive.Load())
	}
	close(release)
	deadline = time.Now().Add(2 * time.Second)
	for started.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond * 5)
	}
	if started.Load() != 6 {
		t.Fatalf("not all sessions restored: %d", started.Load())
	}
}

// This is a bounded synthetic isolation/concurrency smoke test. It makes no
// claim about 50 live provider sessions or production throughput.
func TestSynthetic50AccountsRoutingAndWorkerBounds(t *testing.T) {
	if testing.Short() {
		t.Skip("synthetic 50-account worker smoke")
	}
	ctx := context.Background()
	messagesPerAccount := 2
	if value := os.Getenv("GOOMNI_SYNTHETIC_MESSAGES_PER_ACCOUNT"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 1000 {
			t.Fatal("GOOMNI_SYNTHETIC_MESSAGES_PER_ACCOUNT must be 1..1000")
		}
		messagesPerAccount = n
	}
	expectedSends := 50 * messagesPerAccount
	expectedDeliveries := expectedSends * 2
	var sendsActive, sendsMax, hooksActive, hooksMax, delivered atomic.Int32
	observeMax := func(max *atomic.Int32, n int32) {
		for {
			old := max.Load()
			if n <= old || max.CompareAndSwap(old, n) {
				return
			}
		}
	}
	var seenMu sync.Mutex
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hooksActive.Add(1)
		observeMax(&hooksMax, n)
		defer hooksActive.Add(-1)
		var event domains.Event
		if e := json.NewDecoder(r.Body).Decode(&event); e != nil {
			t.Error(e)
		}
		if event.SessionID == "" || event.AccountID != strings.TrimPrefix(event.SessionID, "account-") {
			t.Errorf("misrouted event %+v", event)
		}
		key := r.URL.Path + ":" + event.ID
		seenMu.Lock()
		if seen[key] {
			t.Error("duplicate delivery without retry", key)
		}
		seen[key] = true
		seenMu.Unlock()
		time.Sleep(time.Millisecond)
		delivered.Add(1)
		w.WriteHeader(204)
	}))
	defer server.Close()
	var s *Service
	factory := func(d domains.Device) domains.Client {
		f := &fakeClient{status: readyStatus()}
		var perAccount atomic.Int32
		f.sendFn = func(ctx context.Context, request domains.SendRequest) (domains.SendResult, error) {
			if perAccount.Add(1) != 1 {
				t.Error("overlapping sends for account", d.ID)
			}
			defer perAccount.Add(-1)
			n := sendsActive.Add(1)
			observeMax(&sendsMax, n)
			defer sendsActive.Add(-1)
			time.Sleep(time.Millisecond)
			event := domains.Event{ID: request.RequestID, Type: "message", AccountID: d.AccountID, Peer: request.Peer, MessageID: request.RequestID, Direction: "outgoing", Payload: json.RawMessage(`{"message":"synthetic"}`)}
			if err := s.sink(d.ConnectionID)(ctx, event); err != nil {
				return domains.SendResult{}, err
			}
			return domains.SendResult{MessageID: request.RequestID, Date: time.Now()}, nil
		}
		return f
	}
	options := Options{QueueLimit: expectedSends + 1, PollInterval: time.Millisecond * 5, SendWorkers: 4, WebhookWorkers: 8, GlobalWebhooks: []storage.WebhookTarget{{URL: server.URL + "/one", Secret: "one"}, {URL: server.URL + "/two", Secret: "two"}}}
	var st *storage.Store
	s, st = testService(t, options, factory)
	type operationRef struct {
		device string
		id     string
	}
	operations := []operationRef{}
	for n := 1; n <= 50; n++ {
		number := fmt.Sprint(n)
		d := mustDevice(t, s, "account-"+number)
		if e := st.SaveSession(ctx, d.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: number, Token: "synthetic-only"}); e != nil {
			t.Fatal(e)
		}
		for m := 1; m <= messagesPerAccount; m++ {
			op, e := s.Send(ctx, d.ID, sendReq(fmt.Sprint("message-", m)), fmt.Sprint("key-", m))
			if e != nil {
				t.Fatal(e)
			}
			operations = append(operations, operationRef{d.ID, op.ID})
		}
	}
	if e := s.Start(ctx); e != nil {
		t.Fatal(e)
	}
	budget := 15 * time.Second
	if extended := time.Duration(expectedDeliveries) * 20 * time.Millisecond; extended > budget {
		budget = extended
	}
	deadline := time.Now().Add(budget)
	for delivered.Load() < int32(expectedDeliveries) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond * 10)
	}
	if delivered.Load() != int32(expectedDeliveries) {
		t.Fatalf("delivered %d/%d; metrics %+v", delivered.Load(), expectedDeliveries, s.WorkerMetrics())
	}
	for _, ref := range operations {
		op, e := s.GetOperation(ctx, ref.device, ref.id)
		if e != nil || op.State != "succeeded" {
			t.Fatalf("operation failed %+v %v", op, e)
		}
	}
	if sendsMax.Load() > 4 || hooksMax.Load() > 8 {
		t.Fatalf("worker bounds exceeded sends=%d hooks=%d", sendsMax.Load(), hooksMax.Load())
	}
	metrics := s.WorkerMetrics()
	if metrics.SendAttempts != uint64(expectedSends) || metrics.SendUnknown != 0 || metrics.WebhookAttempts != uint64(expectedDeliveries) {
		t.Fatalf("unexpected metrics %+v", metrics)
	}
	t.Logf("synthetic: 50 accounts, %d sends, %d webhook deliveries; max active sends=%d webhook=%d", expectedSends, expectedDeliveries, sendsMax.Load(), hooksMax.Load())
}

func TestLogoutCancelsPendingWorkAndInvalidatesEarlierClaim(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{status: readyStatus(), logoutErr: errors.New("remote session revoke unconfirmed")}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "alpha")
	if e := st.SaveSession(ctx, d.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "100", Token: "private"}); e != nil {
		t.Fatal(e)
	}
	first, e := s.Send(ctx, d.ID, sendReq("first"), "one")
	if e != nil {
		t.Fatal(e)
	}
	pending, e := s.Send(ctx, d.ID, sendReq("pending"), "two")
	if e != nil {
		t.Fatal(e)
	}
	request := sendReq("scheduled")
	request.ScheduleOptions = domainSend.ScheduleOptions{ScheduledAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Timezone: "UTC"}
	schedule, e := s.CreateSchedule(ctx, d.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	claimed := claimOne(t, st)
	e = s.Logout(ctx, d.ID)
	codeIs(t, e, "REMOTE_LOGOUT_UNCONFIRMED")
	firstAfter, _ := s.GetOperation(ctx, d.ID, first.ID)
	pendingAfter, _ := s.GetOperation(ctx, d.ID, pending.ID)
	scheduleAfter, _ := s.GetSchedule(ctx, d.ID, schedule.ID)
	if firstAfter.State != "unknown" || pendingAfter.State != "cancelled" || scheduleAfter.State != "cancelled" {
		t.Fatalf("logout transitions first=%s pending=%s schedule=%s", firstAfter.State, pendingAfter.State, scheduleAfter.State)
	}
	// Simulate a worker that had claimed before logout but had not acquired the
	// account lock, followed by immediate reauthentication. It must not send.
	f.mu.Lock()
	f.status = readyStatus()
	f.mu.Unlock()
	s.processOperation(claimed)
	if f.sendCalls != 0 {
		t.Fatal("stale claimed operation sent after logout")
	}
	session, e := st.LoadSession(ctx, d.ConnectionID)
	if e != nil || session != nil {
		t.Fatal("local session survived logout", session, e)
	}
	firstAfter, _ = s.GetOperation(ctx, d.ID, first.ID)
	if firstAfter.State != "unknown" {
		t.Fatal("unknown was rewritten", firstAfter)
	}
}

func TestDeviceWebhookRequiresSecretAndAllowsExplicitDisable(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "webhook")
	url := "https://example.invalid/webhook"
	secret := "not-empty"
	_, err := s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{URL: &url})
	codeIs(t, err, "WEBHOOK_SECRET_REQUIRED")
	if _, err = s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{URL: &url, Secret: &secret}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	_, err = s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{Secret: &empty})
	codeIs(t, err, "WEBHOOK_SECRET_REQUIRED")
	if _, err = s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{URL: &empty, Secret: &empty}); err != nil {
		t.Fatal(err)
	}
}
