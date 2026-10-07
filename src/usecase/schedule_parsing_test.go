package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
)

func TestAcceptedScheduleTimestampMaterializes(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "whitespace")
	r := sendReq("synthetic schedule")
	r.Kind, r.Timezone, r.Recurrence = "text", "UTC", "none"
	r.ScheduledAt = " \t" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + " \n"
	job, err := s.CreateScheduleIdempotent(ctx, d.ID, r, "whitespace-create")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the same parser and transaction the due worker uses, without
	// waiting for a real clock or contacting a provider.
	s.materializeSchedule(job)
	got, err := st.GetSchedule(ctx, d.ConnectionID, job.ID)
	if err != nil || got.State != "completed" || got.Count != 1 {
		t.Fatalf("accepted schedule did not materialize: %+v %v", got, err)
	}
	again, err := s.CreateScheduleIdempotent(ctx, d.ID, r, "whitespace-create")
	if err != nil || again.ID != job.ID || again.State != "completed" {
		t.Fatalf("original request lost idempotency: %+v %v", again, err)
	}
	op := claimOne(t, st)
	if op.Request.Text != r.Text || op.ConnectionID != d.ConnectionID {
		t.Fatalf("incorrect materialized operation: %+v", op)
	}
}

func TestPersistedWhitespaceScheduleKeepsIdempotency(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "persisted-whitespace")
	due := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	r := sendReq("synthetic persisted schedule")
	r.Kind, r.Timezone, r.Recurrence = "text", " UTC ", "once"
	r.ScheduledAt = " " + due.Format(time.RFC3339) + " "
	// Model the exact row an older version accepted and persisted. It is now
	// due, so normal validation of a new schedule would reject its timestamp.
	job, err := st.CreateScheduleIdempotent(ctx, d.ConnectionID, r, due, "persisted-key")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := st.DueSchedules(ctx, time.Now(), 10, time.Time{}, "")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("load persisted due schedule: %v %v", jobs, err)
	}
	s.materializeSchedule(jobs[0])
	s.materializeSchedule(jobs[0])
	got, err := s.CreateScheduleIdempotent(ctx, d.ID, r, "persisted-key")
	if err != nil || got.ID != job.ID || got.State != "completed" || got.Count != 1 || got.Request.ScheduledAt != r.ScheduledAt {
		t.Fatalf("persisted schedule changed or could not complete: %+v %v", got, err)
	}
	changed := r
	changed.ScheduledAt = strings.TrimSpace(r.ScheduledAt)
	_, err = s.CreateScheduleIdempotent(ctx, d.ID, changed, "persisted-key")
	codeIs(t, err, "IDEMPOTENCY_CONFLICT")
	operations, err := st.ClaimOperations(ctx, 10)
	if err != nil || len(operations) != 1 {
		t.Fatalf("persisted occurrence not materialized exactly once: %+v %v", operations, err)
	}
}
