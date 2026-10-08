package rest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatusEndpointsIncludeImmutableIdentityAndSafeLoginSnapshot(t *testing.T) {
	srv, svc := setupAPI(t, "")
	d, err := svc.CreateDevice(context.Background(), "snapshot")
	require.NoError(t, err)
	for _, path := range []string{"/app/status", "/devices/snapshot/status"} {
		status, out := apiRequest(t, srv, "GET", path, d.ID, nil)
		require.Equal(t, 200, status, out)
		result := out["results"].(map[string]any)
		require.Equal(t, d.ID, result["device_id"])
		require.Equal(t, d.InstanceToken(), result["instance_id"])
		require.Equal(t, "", result["account_id"])
		require.Contains(t, result, "challenge")
		require.Nil(t, result["challenge"])
		require.Equal(t, "auth_required", result["auth"])
		require.Equal(t, "disconnected", result["transport"])
		require.Equal(t, "degraded", result["recovery"])
		require.NotEmpty(t, result["server_time"])
	}
}
