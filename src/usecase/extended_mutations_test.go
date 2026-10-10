package usecase

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"testing"
	"time"
)

func TestExtendedMutationUsesSameDurableUnknownAndIdempotencyRules(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{status: readyStatus(), callErr: &domains.Error{Code: "SEND_UNKNOWN", Message: "private provider text", Ambiguous: true, HTTP: 202}}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "extended")
	body := json.RawMessage(`{"peer":{"type":"group","id":"77"},"user":{"type":"user","id":"42"}}`)
	op, err := s.Mutate(ctx, d.ID, "group.promote", body, "promote-once")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Call(ctx, d.ID, "group.promote", body)
	codeIs(t, err, "DURABLE_OPERATION_REQUIRED")
	s.processOperation(claimOne(t, st))
	again, err := s.Mutate(ctx, d.ID, "group.promote", body, "promote-once")
	if err != nil || again.ID != op.ID || again.State != "unknown" {
		t.Fatal(again, err)
	}
	jobs, err := st.ClaimOperations(ctx, 10)
	if err != nil || len(jobs) != 0 || f.callCalls != 1 {
		t.Fatal("ambiguous operation retried", jobs, err, f.callCalls)
	}
}

func TestMediaMutationPinsAccountScopedAsset(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{}, nil)
	a := mustDevice(t, s, "avatar-one")
	b := mustDevice(t, s, "avatar-two")
	asset := domains.Media{ID: "image-id", ConnectionID: a.ConnectionID, Path: "/synthetic/image.png", Size: 4}
	if err := st.SaveMedia(ctx, asset); err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(`{"media_id":"image-id"}`)
	if _, err := s.Mutate(ctx, b.ID, "account.avatar", body, "other-account"); err == nil {
		t.Fatal("cross-account media accepted")
	}
	op, err := s.Mutate(ctx, a.ID, "account.avatar", body, "avatar-once")
	if err != nil || op.Request.MediaID != asset.ID {
		t.Fatal(op, err)
	}
	codeIs(t, st.DeleteMedia(ctx, a.ConnectionID, asset.ID), "MEDIA_IN_USE")
	_ = claimOne(t, st)
	if err := st.FinishOperation(ctx, a.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", ""); err != nil {
		t.Fatal(err)
	}
	codeIs(t, st.DeleteMedia(ctx, a.ConnectionID, asset.ID), "MEDIA_IN_USE")
}

func TestReviewedSendOperationScheduleHasOneDurableOccurrence(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{status: readyStatus()}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "schedule-rich")
	req := domains.SendRequest{Operation: "send.poll", Payload: json.RawMessage(`{"peer":{"type":"group","id":"77"},"question":"Example?","options":["A","B"]}`)}
	req.ScheduledAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	req.Timezone = "UTC"
	job, err := s.CreateScheduleIdempotent(ctx, d.ID, req, "poll-schedule")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateScheduleIdempotent(ctx, d.ID, req, "poll-schedule")
	if err != nil || again.ID != job.ID {
		t.Fatal(again, err)
	}
	if job.Request.Kind != "operation" || job.Request.Peer.ID != "77" {
		t.Fatal(job)
	}
	// The same occurrence cannot materialize twice, including after retries.
	_, err = st.MaterializeSchedule(ctx, d.ConnectionID, job.ID, job.NextAt, nil, storage.AdmissionLimits{Global: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.MaterializeSchedule(ctx, d.ConnectionID, job.ID, job.NextAt, nil, storage.AdmissionLimits{Global: 100})
	jobs, err := st.ClaimOperations(ctx, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	if jobs[0].Request.RequestID == "" || jobs[0].Request.Operation != "send.poll" {
		t.Fatal(jobs)
	}
	s.processOperation(jobs[0])
	if f.callCalls != 1 {
		t.Fatal("scheduled mutation did not use durable call path")
	}
	req.Operation = "group.create"
	_, err = s.CreateScheduleIdempotent(ctx, d.ID, req, "admin-mutation")
	codeIs(t, err, "INVALID_REQUEST")
	req.Operation = "send.poll"
	req.Peer = domains.Peer{Type: "group", ID: "78"}
	_, err = s.CreateScheduleIdempotent(ctx, d.ID, req, "wrong-peer")
	codeIs(t, err, "INVALID_REQUEST")
}
