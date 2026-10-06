package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
)

func TestOversizedVoiceCaptionIsRejectedBeforeSendOrSchedulePersists(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{status: readyStatus()} })
	d := mustDevice(t, s, "voice-caption")
	asset := domains.Media{ID: "voice-fixture", ConnectionID: d.ConnectionID, Name: "voice.ogg", ContentType: "audio/ogg", Path: "unused-fixture", Size: 5, CreatedAt: time.Now()}
	if err := st.SaveMedia(ctx, asset); err != nil {
		t.Fatal(err)
	}
	r := domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Kind: "voice", MediaID: asset.ID, Text: strings.Repeat("a", 65537)}
	_, err := s.Send(ctx, d.ID, r, "oversized-voice-send")
	codeIs(t, err, "INVALID_REQUEST")
	r.Timezone = "UTC"
	r.ScheduledAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	_, err = s.CreateScheduleIdempotent(ctx, d.ID, r, "oversized-voice-schedule")
	codeIs(t, err, "INVALID_REQUEST")
	operations, err := st.ListOperations(ctx, d.ConnectionID, 10, 0)
	if err != nil || len(operations) != 0 {
		t.Fatal("rejected voice persisted as an operation", operations, err)
	}
	schedules, err := st.ListSchedules(ctx, d.ConnectionID, 10, 0)
	if err != nil || len(schedules) != 0 {
		t.Fatal("rejected voice persisted as a schedule", schedules, err)
	}
	r.Text = strings.Repeat("a", 65536)
	if _, err = s.CreateScheduleIdempotent(ctx, d.ID, r, "bounded-voice-schedule"); err != nil {
		t.Fatal("bounded voice caption could not be scheduled", err)
	}
}
