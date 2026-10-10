package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScheduledSendIdempotencySharedAcrossRESTEntryPoints(t *testing.T) {
	s, svc := setupAPI(t, "")
	_, err := svc.CreateDevice(context.Background(), "one", domains.ProviderBale)
	require.NoError(t, err)
	body := map[string]any{"peer": map[string]string{"type": "user", "id": "42"}, "message": "scheduled once", "timezone": "UTC", "scheduled_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	var id string
	for _, path := range []string{"/send/message", "/send/message", "/send/schedules"} {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		r.SetBasicAuth("test", "password")
		r.Header.Set("X-Device-Id", "one")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "same-scheduled-send")
		scopeTestRequest(t, s, r)
		res, err := s.App.Test(r)
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode)
		var response struct {
			Results struct {
				ID         string `json:"id"`
				ScheduleID string `json:"schedule_id"`
			} `json:"results"`
		}
		require.NoError(t, json.NewDecoder(res.Body).Decode(&response))
		res.Body.Close()
		if path != "/send/schedules" {
			response.Results.ID = response.Results.ScheduleID
		}
		require.NotEmpty(t, response.Results.ID)
		if id != "" {
			require.Equal(t, id, response.Results.ID)
		}
		id = response.Results.ID
	}
	schedules, err := svc.ListSchedules(context.Background(), "one", 50, 0)
	require.NoError(t, err)
	require.Len(t, schedules, 1)
}
