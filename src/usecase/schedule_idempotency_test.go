package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
)

func TestScheduleIdempotencyBeforeExpiredTimeValidation(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "scheduled")
	request := sendReq("once")
	request.Timezone = "UTC"
	request.ScheduledAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	first, err := s.CreateScheduleIdempotent(ctx, d.ID, request, "logical-schedule")
	if err != nil {
		t.Fatal(err)
	}
	request.Kind = "text" // same default normalization across REST endpoints
	again, err := s.CreateScheduleIdempotent(ctx, d.ID, request, "logical-schedule")
	if err != nil || again.ID != first.ID {
		t.Fatal("schedule duplicated", again, err)
	}
	changed := request
	changed.Text = "different"
	_, err = s.CreateScheduleIdempotent(ctx, d.ID, changed, "logical-schedule")
	codeIs(t, err, "IDEMPOTENCY_CONFLICT")
	changed.ScheduleOptions = request.ScheduleOptions
	changed.ScheduledAt = ""
	_, err = s.Send(ctx, d.ID, changed, "logical-schedule")
	codeIs(t, err, "IDEMPOTENCY_CONFLICT")
	// Model a persisted schedule originally accepted before its now-past time.
	past := request
	past.ScheduledAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	prior, err := st.CreateScheduleIdempotent(ctx, d.ConnectionID, past, time.Now().Add(-time.Hour), "past-request")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetScheduleState(ctx, d.ConnectionID, prior.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	retried, err := s.CreateScheduleIdempotent(ctx, d.ID, past, "past-request")
	if err != nil || retried.ID != prior.ID || retried.State != "completed" {
		t.Fatal("completed retry failed future-date validation", retried, err)
	}
	_, err = s.CreateScheduleIdempotent(ctx, d.ID, past, "new-past-request")
	codeIs(t, err, "INVALID_SCHEDULE")
	if err = s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	replacement := mustDevice(t, s, d.ID)
	if replacement.ConnectionID == d.ConnectionID {
		t.Fatal("alias rebound old connection")
	}
	fresh, err := s.CreateScheduleIdempotent(ctx, replacement.ID, request, "logical-schedule")
	if err != nil || fresh.ID == first.ID {
		t.Fatal("reused alias inherited idempotency", fresh, err)
	}
}

func TestIdempotentMediaRetriesSurviveRetiredAssets(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{status: readyStatus()} })
	d := mustDevice(t, s, "media")
	asset := domains.Media{ID: "old-asset", ConnectionID: d.ConnectionID, Name: "fixture.txt", Path: "unused-fixture", Size: 5, CreatedAt: time.Now()}
	if err := st.SaveMedia(ctx, asset); err != nil {
		t.Fatal(err)
	}
	request := domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Kind: "file", MediaID: asset.ID}
	op, err := s.Send(ctx, d.ID, request, "finished-file")
	if err != nil {
		t.Fatal(err)
	}
	s.processOperation(claimOne(t, st))
	scheduled := request
	scheduled.Timezone = "UTC"
	scheduled.ScheduledAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	prior, err := st.CreateScheduleIdempotent(ctx, d.ConnectionID, scheduled, time.Now().Add(-time.Hour), "finished-file-schedule")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetScheduleState(ctx, d.ConnectionID, prior.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	if err = st.DeleteMedia(ctx, d.ConnectionID, asset.ID); err != nil {
		t.Fatal(err)
	}
	again, err := s.Send(ctx, d.ID, request, "finished-file")
	if err != nil || again.ID != op.ID || again.State != "succeeded" {
		t.Fatal("completed send lost idempotency after asset retirement", again, err)
	}
	againSchedule, err := s.CreateScheduleIdempotent(ctx, d.ID, scheduled, "finished-file-schedule")
	if err != nil || againSchedule.ID != prior.ID || againSchedule.State != "completed" {
		t.Fatal("completed schedule lost idempotency after asset retirement", againSchedule, err)
	}
	_, err = s.Send(ctx, d.ID, request, "new-file-send")
	codeIs(t, err, "NOT_FOUND")
}
