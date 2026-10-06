package usecase

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
)

type fixtureRoundTripper func(*http.Request) (*http.Response, error)

func (f fixtureRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func claimedWebhook(t *testing.T, transport http.RoundTripper) (*Service, *storage.Store, domains.Delivery) {
	t.Helper()
	s, st := testService(t, Options{WebhookClient: &http.Client{Transport: transport}}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "hook")
	url, secret := "https://example.invalid/events", "synthetic"
	if _, err := s.PatchWebhook(context.Background(), d.ID, domains.WebhookPatch{URL: &url, Secret: &secret}); err != nil {
		t.Fatal(err)
	}
	event := domains.Event{ID: "shutdown-event", Type: "message", Peer: domains.Peer{Type: "user", ID: "42"}, MessageID: "7", Payload: json.RawMessage(`{}`)}
	if err := s.sink(d.ConnectionID)(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ClaimDeliveries(context.Background(), 1, time.Now().Add(time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	return s, st, jobs[0]
}

func TestWebhookCancelledLookupReturnsClaimToRetry(t *testing.T) {
	calls := 0
	s, st, job := claimedWebhook(t, fixtureRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	s.deliver(job)
	saved, err := st.GetDelivery(context.Background(), job.ConnectionID, job.ID)
	if err != nil || saved.State != "retry" || saved.Attempts != 0 || calls != 0 {
		t.Fatalf("cancelled lookup orphaned event or consumed HTTP attempt: %+v calls=%d err=%v", saved, calls, err)
	}
	s.ctx = context.Background()
	jobs, err := st.ClaimDeliveries(context.Background(), 1, time.Now().Add(time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	s.deliver(jobs[0])
	saved, err = st.GetDelivery(context.Background(), job.ConnectionID, job.ID)
	if err != nil || saved.State != "delivered" || calls != 1 {
		t.Fatal(saved, calls, err)
	}
}

func TestWebhookShutdownDuringFinalHTTPAttemptRemainsRecoverable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, st, job := claimedWebhook(t, fixtureRoundTripper(func(*http.Request) (*http.Response, error) { cancel(); return nil, context.Canceled }))
	s.ctx = ctx
	// A shutdown is not evidence that the target failed its last budgeted try.
	job.Attempts = 8
	s.deliver(job)
	saved, err := st.GetDelivery(context.Background(), job.ConnectionID, job.ID)
	if err != nil || saved.State != "retry" {
		t.Fatal(saved, err)
	}
}

func TestWebhookDeletedConnectionDoesNotReviveClaim(t *testing.T) {
	calls := 0
	s, st, job := claimedWebhook(t, fixtureRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, context.Canceled }))
	if err := st.DeleteDevice(context.Background(), job.ConnectionID); err != nil {
		t.Fatal(err)
	}
	s.deliver(job)
	jobs, err := st.ClaimDeliveries(context.Background(), 10, time.Now().Add(time.Hour))
	if err != nil || len(jobs) != 0 || calls != 0 || s.WorkerMetrics().WorkerErrors != 0 {
		t.Fatal(jobs, calls, err, s.WorkerMetrics())
	}
}
