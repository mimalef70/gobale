package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
)

func TestWebhookHTTPPolicyPreservesOrderAndUnblocksPermanentRejections(t *testing.T) {
	for _, tc := range []struct {
		status int
		state  string
	}{
		{204, "delivered"}, {302, "retry"},
		{400, "failed"}, {401, "failed"}, {403, "failed"}, {404, "failed"},
		{409, "failed"}, {410, "failed"}, {413, "failed"}, {415, "failed"}, {422, "failed"},
		{408, "retry"}, {425, "retry"}, {429, "retry"},
		{500, "retry"}, {502, "retry"}, {503, "retry"},
		{0, "retry"}, // Transport errors carry no receiver acknowledgement.
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			s, st, first := claimedWebhook(t, fixtureRoundTripper(func(*http.Request) (*http.Response, error) {
				if tc.status == 0 {
					return nil, errors.New("synthetic private transport detail")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("synthetic private receiver body")), Header: http.Header{}}, nil
			}))
			ctx := context.Background()
			event := domains.Event{ID: "later-event", Type: "message", Peer: domains.Peer{Type: "user", ID: "42"}, MessageID: "8", Payload: json.RawMessage(`{"message":"later"}`)}
			if err := s.sink(first.ConnectionID)(ctx, event); err != nil {
				t.Fatal(err)
			}
			s.deliver(first)
			saved, err := st.GetDelivery(ctx, first.ConnectionID, first.ID)
			if err != nil || saved.State != tc.state || saved.Attempts != 1 {
				t.Fatalf("unexpected outcome: %+v err=%v", saved, err)
			}
			if strings.Contains(saved.LastError, "private") {
				t.Fatal("receiver or transport detail leaked", saved.LastError)
			}
			if tc.state == "failed" && !strings.Contains(saved.LastError, fmt.Sprintf("HTTP %d", tc.status)) {
				t.Fatal("terminal status was not inspectable", saved.LastError)
			}
			at := time.Now().Add(time.Second)
			if tc.state == "retry" {
				at = saved.NextAt.Add(-time.Millisecond)
			}
			jobs, err := st.ClaimDeliveries(ctx, 10, at)
			if err != nil {
				t.Fatal(err)
			}
			if tc.state == "retry" {
				// Later work cannot overtake a transient failure before it is due.
				if len(jobs) != 0 {
					t.Fatalf("later event overtook transient failure: %+v", jobs)
				}
			} else if len(jobs) != 1 || jobs[0].ID == first.ID {
				t.Fatalf("terminal delivery blocked next event: %+v", jobs)
			}
		})
	}
}

func TestWebhookTransientExhaustionPreservesFailedBodyForExplicitReplay(t *testing.T) {
	calls := 0
	s, st, first := claimedWebhook(t, fixtureRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	}))
	ctx := context.Background()
	job := first
	for attempt := 1; attempt <= 8; attempt++ {
		s.deliver(job)
		saved, err := st.GetDelivery(ctx, first.ConnectionID, first.ID)
		if err != nil || saved.Attempts != attempt || !bytes.Equal(first.Body, saved.Body) {
			t.Fatalf("attempt/body changed: %+v err=%v", saved, err)
		}
		if attempt == 8 {
			if saved.State != "failed" || calls != 8 {
				t.Fatalf("retry budget not enforced: %+v calls=%d", saved, calls)
			}
			break
		}
		if saved.State != "retry" {
			t.Fatalf("transient failure terminated early: %+v", saved)
		}
		jobs, err := st.ClaimDeliveries(ctx, 1, time.Now().Add(time.Hour))
		if err != nil || len(jobs) != 1 || jobs[0].ID != first.ID {
			t.Fatal(jobs, err)
		}
		job = jobs[0]
	}
	jobs, err := st.ClaimDeliveries(ctx, 10, time.Now().Add(24*time.Hour))
	if err != nil || len(jobs) != 0 {
		t.Fatal("terminal work retried automatically", jobs, err)
	}
	replays, err := s.ReplayDelivery(ctx, first.DeviceID, first.ID)
	if err != nil || len(replays) != 1 {
		t.Fatal(replays, err)
	}
	replay := replays[0]
	if replay.ID == first.ID || replay.EventID != first.EventID || replay.Attempts != 0 || !bytes.Equal(replay.Body, first.Body) {
		t.Fatal("replay lost identity, body or fresh attempt budget", replay)
	}
	retained, err := st.GetDelivery(ctx, first.ConnectionID, first.ID)
	if err != nil || retained.State != "failed" || retained.Attempts != 8 {
		t.Fatal("replay rewrote terminal audit history", retained, err)
	}
}
