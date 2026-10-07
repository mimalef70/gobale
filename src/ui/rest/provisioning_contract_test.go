package rest

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func TestProvisioningRESTRejectsUnhashedFieldsAndNull(t *testing.T) {
	var providerCalls atomic.Int32
	srv, _ := setupAPIWithFactory(t, "", func(domains.Device) domains.Client {
		providerCalls.Add(1)
		return &testClient{}
	})
	for _, body := range []string{
		`{"device_id":"channel","webhook_filters":{"directions":["incoming"]}}`,
		`{"device_id":"channel","DEVICE_ID":"other"}`,
		`{"device_id":"channel","Webhook_Secret":"ignored"}`,
		`{"device_id":null}`,
		`{"device_id":"channel","webhook_url":null}`,
		`{"device_id":"channel","webhook_secret":null}`,
		`{"device_id":"channel","webhook_events":null}`,
		`{"device_id":"channel","webhook_filter":null}`,
		`{"device_id":"channel","device_id":"other"}`,
	} {
		t.Run(body, func(t *testing.T) {
			response := consumerFlowRaw(t, srv, "POST", "/devices", nil, []byte(body), "application/json", "unconsumed-key")
			defer response.Body.Close()
			require.Equal(t, 400, response.StatusCode)
			devices, err := srv.store.ListDevices(context.Background())
			require.NoError(t, err)
			require.Empty(t, devices, "invalid bodies must not create a device or reserve their key")
		})
	}
	for i, body := range []string{
		`{"device_id":"channel","webhook_url":"https://example.test/hook","webhook_secret":"synthetic-secret"}`,
		`{"webhook_secret":"synthetic-secret","device_id":"channel","webhook_filter":{},"webhook_url":"https://example.test/hook","webhook_events":[]}`,
	} {
		response := consumerFlowRaw(t, srv, "POST", "/devices", nil, []byte(body), "application/json", "unconsumed-key")
		var envelope map[string]any
		require.NoError(t, json.NewDecoder(response.Body).Decode(&envelope))
		require.NoError(t, response.Body.Close())
		require.Equal(t, []int{201, 200}[i], response.StatusCode, envelope)
		config := envelope["results"].(map[string]any)["webhook"].(map[string]any)
		require.NotContains(t, config, "webhook_secret")
	}
	response := consumerFlowRaw(t, srv, "POST", "/devices", nil, []byte(`{"device_id":"channel","webhook_url":"https://example.test/hook","webhook_secret":"synthetic-secret","unknown":true}`), "application/json", "unconsumed-key")
	defer response.Body.Close()
	require.Equal(t, 400, response.StatusCode, "a changed unknown field must not silently become a successful replay")
	require.Zero(t, providerCalls.Load())
}
