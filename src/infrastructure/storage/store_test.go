package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/pkg/sqlite"
	"github.com/stretchr/testify/require"
)

var testKey = bytes.Repeat([]byte{42}, 32)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "goomni.db")
	s, e := Open(p, testKey)
	require.NoError(t, e)
	t.Cleanup(func() { s.Close() })
	return s, p
}
func device(t *testing.T, s *Store, id string) domains.Device {
	t.Helper()
	d, e := s.CreateDevice(context.Background(), id, domains.ProviderBale)
	require.NoError(t, e)
	return d
}
func textRequest(message string) domains.SendRequest {
	return domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "9007199254740993"}, Kind: "text", Text: message}
}
func errorCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var e *domains.Error
	require.True(t, errors.As(err, &e), "unexpected error %v", err)
	require.Equal(t, code, e.Code)
}
func event(id, cp string) domains.Event {
	return domains.Event{ID: id, Type: "message", Peer: domains.Peer{Type: "user", ID: "123"}, MessageID: id, Time: time.Unix(1720000000, 0), Payload: json.RawMessage(`{"text":"hello"}`), Checkpoint: cp}
}
func ptr[T any](v T) *T { return &v }

func TestStoreOwnershipKeyAndForeignDatabase(t *testing.T) {
	s, p := testStore(t)
	_, e := Open(p, testKey)
	require.ErrorContains(t, e, "already owned")
	var mode string
	require.NoError(t, s.db.QueryRow("PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "wal", mode)
	var synchronous int
	require.NoError(t, s.db.QueryRow("PRAGMA synchronous").Scan(&synchronous))
	require.Equal(t, 2, synchronous)
	require.NoError(t, s.Close())
	_, e = Open(p, bytes.Repeat([]byte{43}, 32))
	require.ErrorContains(t, e, "does not match")
	_, e = Open(filepath.Join(t.TempDir(), "x.db"), []byte("short"))
	require.ErrorContains(t, e, "32 bytes")
	foreign := filepath.Join(t.TempDir(), "foreign.db")
	db, e := sql.Open(sqlite.DriverName, foreign)
	require.NoError(t, e)
	_, e = db.Exec("CREATE TABLE unrelated (id TEXT)")
	require.NoError(t, e)
	require.NoError(t, db.Close())
	before, e := os.ReadFile(foreign)
	require.NoError(t, e)
	_, e = Open(foreign, testKey)
	require.ErrorContains(t, e, "unrelated database")
	after, e := os.ReadFile(foreign)
	require.NoError(t, e)
	require.Equal(t, before, after, "foreign database was modified")
}
func TestDeviceAndSessionIsolationAndEncryption(t *testing.T) {
	s, p := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	b := device(t, s, "beta")
	require.NotEqual(t, a.ConnectionID, b.ConnectionID)
	_, e := s.CreateDevice(ctx, "alpha", domains.ProviderBale)
	errorCode(t, e, "DEVICE_EXISTS")
	_, e = s.GetDevice(ctx, "missing")
	errorCode(t, e, "NOT_FOUND")
	session := &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "123", Token: "PRIVATE-SESSION-TOKEN", DeviceHash: "PRIVATE-DEVICE-HASH"}
	require.NoError(t, s.SaveSession(ctx, a.ConnectionID, session))
	got, e := s.LoadSession(ctx, a.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, session, got)
	got, e = s.LoadSession(ctx, b.ConnectionID)
	require.NoError(t, e)
	require.Nil(t, got)
	require.NoError(t, s.ClearSession(ctx, a.ConnectionID))
	errorCode(t, s.SaveSession(ctx, a.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "456", Token: "other"}), "ACCOUNT_CONFLICT")
	require.NoError(t, s.SaveSession(ctx, a.ConnectionID, session))
	_, e = s.PatchWebhook(ctx, a.ConnectionID, domains.WebhookPatch{URL: ptr("https://example.test/hook"), Secret: ptr("PRIVATE-WEBHOOK-SECRET")})
	require.NoError(t, e)
	require.NoError(t, s.Close())
	raw, e := os.ReadFile(p)
	require.NoError(t, e)
	for _, secret := range []string{session.Token, session.DeviceHash, "PRIVATE-WEBHOOK-SECRET"} {
		require.NotContains(t, string(raw), secret)
	}
	reopened, e := Open(p, testKey)
	require.NoError(t, e)
	defer reopened.Close()
	got, e = reopened.LoadSession(ctx, a.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, session, got)
	require.NoError(t, reopened.DeleteDevice(ctx, a.ConnectionID))
	replacement := device(t, reopened, "alpha")
	require.NotEqual(t, a.ConnectionID, replacement.ConnectionID)
	_, e = reopened.LoadSession(ctx, a.ConnectionID)
	errorCode(t, e, "NOT_FOUND")
	newSession, e := reopened.LoadSession(ctx, replacement.ConnectionID)
	require.NoError(t, e)
	require.Nil(t, newSession)
}
func TestOutboxIdempotencyClaimAndRestart(t *testing.T) {
	s, p := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	b := device(t, s, "beta")
	first, created, e := s.Enqueue(ctx, a.ConnectionID, textRequest("one"), "same-key", AdmissionLimits{Global: 10})
	require.NoError(t, e)
	require.True(t, created)
	require.NotEmpty(t, first.Request.RequestID)
	duplicate, created, e := s.Enqueue(ctx, a.ConnectionID, textRequest("one"), "same-key", AdmissionLimits{Global: 10})
	require.NoError(t, e)
	require.False(t, created)
	require.Equal(t, first.ID, duplicate.ID)
	_, _, e = s.Enqueue(ctx, a.ConnectionID, textRequest("two"), "same-key", AdmissionLimits{Global: 10})
	errorCode(t, e, "IDEMPOTENCY_CONFLICT")
	second, _, e := s.Enqueue(ctx, a.ConnectionID, textRequest("two"), "different-key", AdmissionLimits{Global: 10})
	require.NoError(t, e)
	_, _, e = s.Enqueue(ctx, b.ConnectionID, textRequest("one"), "same-key", AdmissionLimits{Global: 10})
	require.NoError(t, e)
	claimed, e := s.ClaimOperations(ctx, 10)
	require.NoError(t, e)
	require.Len(t, claimed, 2)
	conns := map[string]bool{}
	for _, op := range claimed {
		require.False(t, conns[op.ConnectionID])
		conns[op.ConnectionID] = true
		require.Equal(t, "sending", op.State)
	}
	empty, e := s.ClaimOperations(ctx, 10)
	require.NoError(t, e)
	require.Empty(t, empty)
	_, e = s.GetOperation(ctx, b.ConnectionID, first.ID)
	errorCode(t, e, "NOT_FOUND")
	require.NoError(t, s.Close())
	s, e = Open(p, testKey)
	require.NoError(t, e)
	defer s.Close()
	for _, op := range claimed {
		got, e := s.GetOperation(ctx, op.ConnectionID, op.ID)
		require.NoError(t, e)
		require.Equal(t, "unknown", got.State)
		errorCode(t, s.FinishOperation(ctx, op.ConnectionID, op.ID, "queued", nil, "", ""), "OPERATION_CONFLICT")
	}
	remaining, e := s.ClaimOperations(ctx, 10)
	require.NoError(t, e)
	require.Len(t, remaining, 1)
	// Which same-millisecond operation was selected is deliberately unspecified.
	require.Equal(t, a.ConnectionID, remaining[0].ConnectionID)
	op, e := s.GetOperation(ctx, a.ConnectionID, second.ID)
	require.NoError(t, e)
	require.NotEmpty(t, op.Request.RequestID)
	require.NoError(t, s.FinishOperation(ctx, remaining[0].ConnectionID, remaining[0].ID, "succeeded", &domains.SendResult{MessageID: "9007199254740993"}, "", ""))
	done, e := s.GetOperation(ctx, remaining[0].ConnectionID, remaining[0].ID)
	require.NoError(t, e)
	require.Equal(t, "succeeded", done.State)
}
func TestOutboxCapacityAndDeletedAliasCannotReceiveOldJobs(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	op, _, e := s.Enqueue(ctx, a.ConnectionID, textRequest("hello"), "k", AdmissionLimits{Global: 1})
	require.NoError(t, e)
	_, _, e = s.Enqueue(ctx, a.ConnectionID, textRequest("more"), "k2", AdmissionLimits{Global: 1})
	errorCode(t, e, "QUEUE_FULL")
	same, created, e := s.Enqueue(ctx, a.ConnectionID, textRequest("hello"), "k", AdmissionLimits{Global: 1})
	require.NoError(t, e)
	require.False(t, created)
	require.Equal(t, op.ID, same.ID)
	require.NoError(t, s.DeleteDevice(ctx, a.ConnectionID))
	replacement := device(t, s, "alpha")
	_, e = s.GetOperation(ctx, replacement.ConnectionID, op.ID)
	errorCode(t, e, "NOT_FOUND")
	claimed, e := s.ClaimOperations(ctx, 10)
	require.NoError(t, e)
	require.Empty(t, claimed)
	_, _, e = s.Enqueue(ctx, a.ConnectionID, textRequest("zombie"), "k3", AdmissionLimits{Global: 10})
	errorCode(t, e, "NOT_FOUND")
}
func TestConcurrentEnqueueAndClaimOnePerDevice(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op, _, e := s.Enqueue(ctx, a.ConnectionID, textRequest("same"), "k", AdmissionLimits{Global: 100})
			if e != nil {
				errs <- e
				return
			}
			ids <- op.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	require.Len(t, unique, 1)
	_, _, e := s.Enqueue(ctx, a.ConnectionID, textRequest("second"), "second", AdmissionLimits{Global: 100})
	require.NoError(t, e)
	claims := make(chan []domains.Operation, 10)
	errs = make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ops, e := s.ClaimOperations(ctx, 50)
			if e != nil {
				errs <- e
			}
			claims <- ops
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	n := 0
	for ops := range claims {
		n += len(ops)
	}
	require.Equal(t, 1, n)
}
func TestEventCheckpointAndDeliveryAtomicity(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	require.NoError(t, s.BindAccount(ctx, d.ConnectionID, "888"))
	target := WebhookTarget{URL: "https://example.test/hook", Secret: "secret"}
	inserted, e := s.AppendEvent(ctx, d.ConnectionID, event("1", "cp1"), []WebhookTarget{target})
	require.NoError(t, e)
	require.True(t, inserted)
	inserted, e = s.AppendEvent(ctx, d.ConnectionID, event("2", "cp2"), []WebhookTarget{target})
	require.NoError(t, e)
	require.True(t, inserted)
	inserted, e = s.AppendEvent(ctx, d.ConnectionID, event("1", "cp1"), []WebhookTarget{target})
	require.NoError(t, e)
	require.False(t, inserted)
	cp, e := s.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, "cp2", cp)
	_, e = s.AppendEvent(ctx, d.ConnectionID, event("3", "cp3"), []WebhookTarget{{URL: "not-a-url"}})
	errorCode(t, e, "INVALID_WEBHOOK_URL")
	cp, e = s.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, "cp2", cp)
	count, e := s.EventCount(ctx, d.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, 2, count)
	deliveries, e := s.ListDeliveries(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, e)
	require.Len(t, deliveries, 2)
	events, e := s.ListEvents(ctx, d.ConnectionID, "user:123", 1, 0)
	require.NoError(t, e)
	require.Len(t, events, 1)
	require.Equal(t, "alpha", events[0].SessionID)
	require.Equal(t, "888", events[0].AccountID)
	chats, e := s.ListChats(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, e)
	require.Len(t, chats, 1)
	require.Equal(t, 2, chats[0].Count)
	other := device(t, s, "beta")
	_, e = s.AppendEvent(ctx, other.ConnectionID, event("1", "cp1"), nil)
	require.NoError(t, e)
	otherEvents, e := s.ListEvents(ctx, other.ConnectionID, "", 50, 0)
	require.NoError(t, e)
	require.Len(t, otherEvents, 1)
	require.NotEqual(t, deliveries[0].EventID, otherEvents[0].ID)
}
func TestWebhookRotationPauseReplayAndRestart(t *testing.T) {
	s, p := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	cfg, e := s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{URL: ptr("https://old.example.test/hook"), Secret: ptr("initial-secret")})
	require.NoError(t, e)
	oldTarget := WebhookTarget{URL: cfg.URL, Secret: cfg.Secret, Revision: cfg.Revision, Device: true}
	_, e = s.AppendEvent(ctx, d.ConnectionID, event("1", "cp1"), []WebhookTarget{oldTarget})
	require.NoError(t, e)
	rotated, e := s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{Secret: ptr("rotated-secret")})
	require.NoError(t, e)
	require.Equal(t, cfg.Revision, rotated.Revision)
	claimed, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	require.Equal(t, "rotated-secret", claimed[0].Secret)
	require.Equal(t, 1, claimed[0].Attempts)
	_, e = s.AppendEvent(ctx, d.ConnectionID, event("2", "cp2"), []WebhookTarget{oldTarget})
	require.NoError(t, e)
	changed, e := s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{URL: ptr("https://new.example.test/hook")})
	require.NoError(t, e)
	require.Greater(t, changed.Revision, cfg.Revision)
	// An in-flight failure cannot resume delivery to the former URL.
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, claimed[0].ID, "retry", time.Now(), "timeout"))
	later, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Empty(t, later)
	backlog, e := s.ListDeliveries(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, e)
	require.Len(t, backlog, 2)
	for _, v := range backlog {
		require.Equal(t, "paused", v.State)
	}
	errorCode(t, s.UpdateDelivery(ctx, d.ConnectionID, backlog[0].ID, "retry", time.Now(), ""), "DELIVERY_CONFLICT")
	replay, e := s.ReplayDelivery(ctx, d.ConnectionID, backlog[0].ID, []WebhookTarget{{URL: changed.URL, Secret: changed.Secret, Revision: changed.Revision, Device: true}})
	require.NoError(t, e)
	require.Len(t, replay, 1)
	require.Equal(t, backlog[0].EventID, replay[0].EventID)
	require.NotEqual(t, backlog[0].ID, replay[0].ID)
	attempt, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Len(t, attempt, 1)
	require.NoError(t, s.Close())
	s, e = Open(p, testKey)
	require.NoError(t, e)
	defer s.Close()
	retry, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Len(t, retry, 1)
	require.Equal(t, attempt[0].ID, retry[0].ID)
	require.Equal(t, 2, retry[0].Attempts)
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, retry[0].ID, "delivered", time.Now(), ""))
}
func TestGlobalRoutingRevisionAndEncryptedSecret(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	_, e := s.AppendEvent(ctx, d.ConnectionID, event("1", "cp1"), []WebhookTarget{{URL: "http://internal/hook", Secret: "global-secret"}})
	require.NoError(t, e)
	var cipher []byte
	require.NoError(t, s.db.QueryRow("SELECT secret FROM deliveries").Scan(&cipher))
	require.NotContains(t, string(cipher), "global-secret")
	claimed, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	require.Equal(t, "global-secret", claimed[0].Secret)
	_, e = s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{URL: ptr("https://override/hook")})
	require.NoError(t, e)
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, claimed[0].ID, "retry", time.Now(), "temporary"))
	retry, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Empty(t, retry)
}
func TestScheduleOccurrenceAtomicityCapacityAndPause(t *testing.T) {
	s, p := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	due := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	next := due.Add(time.Hour)
	sch, e := s.CreateSchedule(ctx, d.ConnectionID, textRequest("scheduled"), due)
	require.NoError(t, e)
	listed, e := s.DueSchedules(ctx, time.Now(), 50, time.Time{}, "")
	require.NoError(t, e)
	require.Len(t, listed, 1)
	require.NoError(t, s.SetScheduleState(ctx, d.ConnectionID, sch.ID, "paused"))
	_, e = s.MaterializeSchedule(ctx, d.ConnectionID, sch.ID, due, &next, AdmissionLimits{Global: 100})
	errorCode(t, e, "SCHEDULE_CONFLICT")
	require.NoError(t, s.SetScheduleState(ctx, d.ConnectionID, sch.ID, "active"))
	op, e := s.MaterializeSchedule(ctx, d.ConnectionID, sch.ID, due, &next, AdmissionLimits{Global: 100})
	require.NoError(t, e)
	require.Equal(t, "queued", op.State)
	_, e = s.MaterializeSchedule(ctx, d.ConnectionID, sch.ID, due, &next, AdmissionLimits{Global: 100})
	errorCode(t, e, "SCHEDULE_CONFLICT")
	_, e = s.MaterializeSchedule(ctx, d.ConnectionID, sch.ID, next, nil, AdmissionLimits{Global: 1})
	errorCode(t, e, "QUEUE_FULL")
	unchanged, e := s.GetSchedule(ctx, d.ConnectionID, sch.ID)
	require.NoError(t, e)
	require.Equal(t, 1, unchanged.Count)
	require.True(t, next.Equal(unchanged.NextAt))
	require.NoError(t, s.Close())
	s, e = Open(p, testKey)
	require.NoError(t, e)
	defer s.Close()
	final, e := s.MaterializeSchedule(ctx, d.ConnectionID, sch.ID, next, nil, AdmissionLimits{Global: 100})
	require.NoError(t, e)
	require.NotEqual(t, op.ID, final.ID)
	completed, e := s.GetSchedule(ctx, d.ConnectionID, sch.ID)
	require.NoError(t, e)
	require.Equal(t, "completed", completed.State)
	require.Equal(t, 2, completed.Count)
	errorCode(t, s.SetScheduleState(ctx, d.ConnectionID, sch.ID, "active"), "SCHEDULE_CONFLICT")
}
func TestMediaIsolationAndPendingPin(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	b := device(t, s, "beta")
	media := domains.Media{ID: "file-1", ConnectionID: a.ConnectionID, Name: "sample.png", ContentType: "image/png", Size: 30, Path: "/private/staged/file-1"}
	require.NoError(t, s.SaveMedia(ctx, media))
	got, e := s.GetMedia(ctx, a.ConnectionID, media.ID)
	require.NoError(t, e)
	require.Equal(t, media.Path, got.Path)
	_, e = s.GetMedia(ctx, b.ConnectionID, media.ID)
	errorCode(t, e, "NOT_FOUND")
	req := textRequest("")
	req.Kind = "image"
	req.MediaID = media.ID
	_, _, e = s.Enqueue(ctx, a.ConnectionID, req, "image", AdmissionLimits{Global: 100})
	require.NoError(t, e)
	errorCode(t, s.DeleteMedia(ctx, a.ConnectionID, media.ID), "MEDIA_IN_USE")
	ops, e := s.ClaimOperations(ctx, 50)
	require.NoError(t, e)
	require.Len(t, ops, 1)
	require.NoError(t, s.FinishOperation(ctx, a.ConnectionID, ops[0].ID, "failed", nil, "BAD_MEDIA", "rejected"))
	require.NoError(t, s.DeleteMedia(ctx, a.ConnectionID, media.ID))
	_, e = s.GetMedia(ctx, a.ConnectionID, media.ID)
	errorCode(t, e, "NOT_FOUND")
}
func TestWebhookURLAndAliasValidation(t *testing.T) {
	for _, bad := range []string{"ftp://example.com", "https://user:pass@example.com/", "https://example.com/#fragment", "/relative"} {
		require.Error(t, ValidateWebhookURL(bad))
	}
	for _, good := range []string{"", "http://127.0.0.1:8080/hook", "https://example.com/hook?q=x"} {
		require.NoError(t, ValidateWebhookURL(good))
	}
	s, _ := testStore(t)
	for _, bad := range []string{"", "..", "/../../", "contains space", strings.Repeat("a", 65)} {
		_, e := s.CreateDevice(context.Background(), bad, domains.ProviderBale)
		errorCode(t, e, "INVALID_DEVICE_ID")
	}
}

func TestDeliveryOrderingDoesNotBlockOtherTargets(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	b := device(t, s, "beta")
	slow := WebhookTarget{URL: "https://slow.example.test"}
	fast := WebhookTarget{URL: "https://fast.example.test"}
	for _, id := range []string{"first", "second"} {
		_, e := s.AppendEvent(ctx, a.ConnectionID, event(id, id), []WebhookTarget{slow, fast})
		require.NoError(t, e)
	}
	_, e := s.AppendEvent(ctx, b.ConnectionID, event("another-account", "cp"), []WebhookTarget{slow})
	require.NoError(t, e)
	jobs, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Len(t, jobs, 3)
	var slowID string
	for _, job := range jobs {
		if job.ConnectionID == a.ConnectionID && job.URL == slow.URL {
			slowID = job.ID
			require.NoError(t, s.UpdateDelivery(ctx, job.ConnectionID, job.ID, "retry", time.Now().Add(time.Hour), "slow"))
		} else {
			require.NoError(t, s.UpdateDelivery(ctx, job.ConnectionID, job.ID, "delivered", time.Now(), ""))
		}
	}
	next, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Len(t, next, 1)
	require.Equal(t, fast.URL, next[0].URL)
	require.NoError(t, s.UpdateDelivery(ctx, next[0].ConnectionID, next[0].ID, "delivered", time.Now(), ""))
	future, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(2*time.Hour))
	require.NoError(t, e)
	require.Len(t, future, 1)
	require.Equal(t, slowID, future[0].ID)
	require.NoError(t, s.UpdateDelivery(ctx, future[0].ConnectionID, future[0].ID, "failed", time.Now(), "exhausted"))
	unblocked, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(2*time.Hour))
	require.NoError(t, e)
	require.Len(t, unblocked, 1)
	require.Equal(t, slow.URL, unblocked[0].URL)
	require.NotEqual(t, slowID, unblocked[0].ID)
}

func TestMediaMustBelongToQueuedAccount(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	b := device(t, s, "beta")
	require.NoError(t, s.SaveMedia(ctx, domains.Media{ID: "private", ConnectionID: a.ConnectionID, Path: "/a/private", Size: 1}))
	req := textRequest("")
	req.Kind = "file"
	req.MediaID = "private"
	_, _, e := s.Enqueue(ctx, b.ConnectionID, req, "key", AdmissionLimits{Global: 100})
	errorCode(t, e, "NOT_FOUND")
	_, e = s.CreateSchedule(ctx, b.ConnectionID, req, time.Now().Add(time.Hour))
	errorCode(t, e, "NOT_FOUND")
	require.NoError(t, s.DeleteMedia(ctx, a.ConnectionID, "private"))
	_, _, e = s.Enqueue(ctx, a.ConnectionID, req, "key", AdmissionLimits{Global: 100})
	errorCode(t, e, "NOT_FOUND")
}

func TestColdBackupRestore(t *testing.T) {
	s, path := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	session := &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "789", Token: "restore-token"}
	require.NoError(t, s.SaveSession(ctx, a.ConnectionID, session))
	_, e := s.AppendEvent(ctx, a.ConnectionID, event("first", "durable-cursor"), nil)
	require.NoError(t, e)
	queued, _, e := s.Enqueue(ctx, a.ConnectionID, textRequest("restore queued send"), "key", AdmissionLimits{Global: 100})
	require.NoError(t, e)
	require.NoError(t, s.Close())
	copyPath := filepath.Join(t.TempDir(), "restored.db")
	raw, e := os.ReadFile(path)
	require.NoError(t, e)
	require.NoError(t, os.WriteFile(copyPath, raw, 0600))
	restored, e := Open(copyPath, testKey)
	require.NoError(t, e)
	defer restored.Close()
	restoredDevice, e := restored.GetDevice(ctx, "alpha")
	require.NoError(t, e)
	require.Equal(t, a.ConnectionID, restoredDevice.ConnectionID)
	restoredSession, e := restored.LoadSession(ctx, a.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, session, restoredSession)
	cp, e := restored.Checkpoint(ctx, a.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, "durable-cursor", cp)
	op, e := restored.GetOperation(ctx, a.ConnectionID, queued.ID)
	require.NoError(t, e)
	require.Equal(t, "queued", op.State)
	require.Equal(t, queued.Request.RequestID, op.Request.RequestID)
}

func TestLogoutStopsAcceptedWorkButKeepsBinding(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	require.NoError(t, s.SaveSession(ctx, d.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "123", Token: "session"}))
	first, _, e := s.Enqueue(ctx, d.ConnectionID, textRequest("first"), "first", AdmissionLimits{Global: 100})
	require.NoError(t, e)
	claimed, e := s.ClaimOperations(ctx, 1)
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	queued, _, e := s.Enqueue(ctx, d.ConnectionID, textRequest("second"), "second", AdmissionLimits{Global: 100})
	require.NoError(t, e)
	scheduled, e := s.CreateSchedule(ctx, d.ConnectionID, textRequest("scheduled"), time.Now().Add(time.Hour))
	require.NoError(t, e)
	require.NoError(t, s.LogoutConnection(ctx, d.ConnectionID))
	session, e := s.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, e)
	require.Nil(t, session)
	a, e := s.GetOperation(ctx, d.ConnectionID, first.ID)
	require.NoError(t, e)
	require.Equal(t, "unknown", a.State)
	b, e := s.GetOperation(ctx, d.ConnectionID, queued.ID)
	require.NoError(t, e)
	require.Equal(t, "cancelled", b.State)
	require.Equal(t, "ACCOUNT_LOGGED_OUT", b.ErrorCode)
	c, e := s.GetSchedule(ctx, d.ConnectionID, scheduled.ID)
	require.NoError(t, e)
	require.Equal(t, "cancelled", c.State)
	errorCode(t, s.FinishOperation(ctx, d.ConnectionID, first.ID, "queued", nil, "", ""), "OPERATION_CONFLICT")
	errorCode(t, s.SaveSession(ctx, d.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "different", Token: "new-session"}), "ACCOUNT_CONFLICT")
}

func TestOnlineBackupIsRestorableAndNeverOverwrites(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	require.NoError(t, s.SaveSession(ctx, d.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "123", Token: "backup-secret"}))
	_, e := s.AppendEvent(ctx, d.ConnectionID, event("first", "cp1"), []WebhookTarget{{URL: "http://internal/hook", Secret: "hook-secret"}})
	require.NoError(t, e)
	pending, _, e := s.Enqueue(ctx, d.ConnectionID, textRequest("durable"), "key", AdmissionLimits{Global: 100})
	require.NoError(t, e)
	backup := filepath.Join(t.TempDir(), "snapshot.db")
	require.NoError(t, s.Backup(ctx, backup))
	data, e := os.ReadFile(backup)
	require.NoError(t, e)
	require.NotContains(t, string(data), "backup-secret")
	require.NotContains(t, string(data), "hook-secret")
	require.Error(t, s.Backup(ctx, backup))
	unchanged, e := os.ReadFile(backup)
	require.NoError(t, e)
	require.Equal(t, data, unchanged)
	// Source remains writable while the restored instance is independent.
	_, e = s.AppendEvent(ctx, d.ConnectionID, event("second", "cp2"), nil)
	require.NoError(t, e)
	restored, e := Open(backup, testKey)
	require.NoError(t, e)
	defer restored.Close()
	cp, e := restored.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, "cp1", cp)
	session, e := restored.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, e)
	require.Equal(t, "backup-secret", session.Token)
	op, e := restored.GetOperation(ctx, d.ConnectionID, pending.ID)
	require.NoError(t, e)
	require.Equal(t, "queued", op.State)
	deliveries, e := restored.ListDeliveries(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, e)
	require.Len(t, deliveries, 1)
	require.Equal(t, "hook-secret", deliveries[0].Secret)
	stat, e := os.Stat(backup)
	require.NoError(t, e)
	require.Equal(t, os.FileMode(0600), stat.Mode().Perm())
}

func TestManualRetryOnlyFailedTargetAndCancelsOriginal(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	_, e := s.AppendEvent(ctx, d.ConnectionID, event("event", "cp"), []WebhookTarget{{URL: "https://one.test"}, {URL: "https://two.test"}})
	require.NoError(t, e)
	jobs, e := s.ClaimDeliveries(ctx, 2, time.Now().Add(time.Second))
	require.NoError(t, e)
	require.Len(t, jobs, 2)
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, jobs[0].ID, "retry", time.Now().Add(time.Hour), "failed attempt"))
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, jobs[1].ID, "delivered", time.Now(), ""))
	retry, e := s.RetryDelivery(ctx, d.ConnectionID, jobs[0].ID)
	require.NoError(t, e)
	require.Equal(t, jobs[0].URL, retry.URL)
	require.NotEqual(t, jobs[0].ID, retry.ID)
	require.Zero(t, retry.Attempts)
	old, e := s.GetDelivery(ctx, d.ConnectionID, jobs[0].ID)
	require.NoError(t, e)
	require.Equal(t, "cancelled", old.State)
	require.Equal(t, 1, old.Attempts)
	require.Equal(t, "failed attempt", old.LastError)
	_, e = s.RetryDelivery(ctx, d.ConnectionID, jobs[0].ID)
	errorCode(t, e, "DELIVERY_CONFLICT")
	_, e = s.RetryDelivery(ctx, d.ConnectionID, jobs[1].ID)
	errorCode(t, e, "DELIVERY_CONFLICT")
	claimed, e := s.ClaimDeliveries(ctx, 8, time.Now().Add(2*time.Hour))
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	require.Equal(t, retry.ID, claimed[0].ID)
}
