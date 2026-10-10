package rest

import (
	"context"
	"github.com/mimalef70/goomni/src/domains"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScheduleOneTimeRecurrenceRESTContract(t *testing.T) {
	s, svc := setupAPI(t, "")
	_, err := svc.CreateDevice(context.Background(), "one", domains.ProviderBale)
	require.NoError(t, err)
	for _, path := range []string{"/send/schedules", "/send/message"} {
		for _, recurrence := range []string{"none", ""} {
			t.Run(path+"/"+recurrence, func(t *testing.T) {
				status, body := apiRequest(t, s, "POST", path, "one", map[string]any{
					"peer":    map[string]string{"type": "user", "id": "42"},
					"message": "synthetic scheduled once", "timezone": "UTC",
					"scheduled_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					"recurrence":   recurrence,
				})
				require.Equal(t, 200, status, "one-time recurrence rejected: %v", body)
				results := body["results"].(map[string]any)
				if path == "/send/message" {
					require.Equal(t, "Message scheduled", results["status"])
					require.NotEmpty(t, results["schedule_id"])
					require.NotContains(t, results, "message_id")
				} else {
					require.Equal(t, "active", results["status"])
				}
			})
		}
		status, body := apiRequest(t, s, "POST", path, "one", map[string]any{
			"peer":    map[string]string{"type": "user", "id": "42"},
			"message": "synthetic scheduled once", "timezone": "UTC",
			"scheduled_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"recurrence":   "once",
		})
		require.Equal(t, 400, status, "legacy recurrence admitted: %v", body)
	}
}
