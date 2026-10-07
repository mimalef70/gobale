package rest

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConflictingRepeatedSelectorsNeverChooseAnAccount(t *testing.T) {
	s, svc := setupAPI(t, "")
	a, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	b, err := svc.CreateDevice(context.Background(), "two")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, path, secondDevice, secondInstance, code string
		status                                         int
	}{
		{"query", "/app/status?device_id=one&device_id=two", "", "", "DEVICE_SELECTOR_CONFLICT", 400},
		{"header", "/app/status", "two", "", "DEVICE_SELECTOR_CONFLICT", 400},
		{"instance", "/app/status", "", b.InstanceToken(), "DEVICE_INSTANCE_CHANGED", 409},
		{"empty-query", "/devices/one/status?device_id=", "", "", "DEVICE_SELECTOR_CONFLICT", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.path, nil)
			r.SetBasicAuth("test", "password")
			r.Header.Set("X-Device-Id", a.ID)
			r.Header.Set("X-Device-Instance", a.InstanceToken())
			if tc.secondDevice != "" {
				r.Header.Add("X-Device-Id", tc.secondDevice)
			}
			if tc.secondInstance != "" {
				r.Header.Add("X-Device-Instance", tc.secondInstance)
			}
			res, err := s.App.Test(r)
			require.NoError(t, err)
			defer res.Body.Close()
			var body map[string]any
			require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
			require.Equal(t, tc.status, res.StatusCode)
			require.Equal(t, tc.code, body["code"])
		})
	}
}
