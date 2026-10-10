package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

type voiceRESTClient struct {
	testClient
	calls *atomic.Int32
}

func (c *voiceRESTClient) Send(_ context.Context, request domains.SendRequest) (domains.SendResult, error) {
	if request.Kind != "voice" || request.RequestID == "" || request.MediaID == "" || request.ReplyMessageID != "-777" {
		return domains.SendResult{}, domains.E("TEST_BAD_VOICE_REQUEST", "voice request was not journaled correctly", 500)
	}
	c.calls.Add(1)
	return domains.SendResult{MessageID: request.RequestID, Date: time.Now().UTC()}, nil
}

func TestVoiceRouteUsesScopedMediaDurableIdempotencyAndSchedule(t *testing.T) {
	var calls atomic.Int32
	srv, svc := setupAPIWithFactory(t, "", func(domains.Device) domains.Client { return &voiceRESTClient{calls: &calls} })
	ctx := context.Background()
	one, err := svc.CreateDevice(ctx, "one", domains.ProviderBale)
	require.NoError(t, err)
	_, err = svc.CreateDevice(ctx, "two", domains.ProviderBale)
	require.NoError(t, err)
	challenge, err := svc.StartAuth(ctx, "one", "+10000000000")
	require.NoError(t, err)
	_, err = svc.SubmitCode(ctx, "one", challenge.ID, "synthetic")
	require.NoError(t, err)
	challenge, err = svc.StartAuth(ctx, "two", "+10000000000")
	require.NoError(t, err)
	_, err = svc.SubmitCode(ctx, "two", challenge.ID, "synthetic")
	require.NoError(t, err)
	// The protocol tests validate real Ogg bytes; this REST test isolates durable routing.
	err = srv.store.SaveMedia(ctx, domains.Media{ID: "voice-fixture", ConnectionID: one.ConnectionID, Name: "voice.ogg", ContentType: "audio/ogg", Size: 12, Path: "unused", CreatedAt: time.Now()})
	require.NoError(t, err)
	body := `{"peer":{"type":"user","id":"42"},"media_id":"voice-fixture","reply_message_id":"-777"}`
	var first domains.Operation
	for _, device := range []string{"two", "one", "one"} {
		r := httptest.NewRequest("POST", "/send/voice", bytes.NewBufferString(body))
		r.SetBasicAuth("test", "password")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Device-Id", device)
		r.Header.Set("Idempotency-Key", "one-voice")
		scopeTestRequest(t, srv, r)
		res, err := srv.App.Test(r)
		require.NoError(t, err)
		if device == "two" {
			require.Equal(t, 404, res.StatusCode)
			_ = res.Body.Close()
			continue
		}
		require.Equal(t, 200, res.StatusCode)
		var response struct {
			Results domains.Operation `json:"results"`
		}
		require.NoError(t, json.NewDecoder(res.Body).Decode(&response))
		_ = res.Body.Close()
		require.Equal(t, "succeeded", response.Results.State)
		if first.ID == "" {
			first = response.Results
		} else {
			require.Equal(t, first.ID, response.Results.ID)
		}
	}
	require.EqualValues(t, 1, calls.Load())
	scheduled := first.Request
	scheduled.RequestID = ""
	scheduled.ScheduledAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	scheduled.Timezone = "UTC"
	job, err := svc.CreateScheduleIdempotent(ctx, "one", scheduled, "scheduled-voice")
	require.NoError(t, err)
	require.Equal(t, "voice", job.Request.Kind)
	require.Equal(t, "voice-fixture", job.Request.MediaID)
}
