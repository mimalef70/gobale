package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func TestProviderNullBodyReturnsErrorBeforePathOrQueryInjection(t *testing.T) {
	s, svc := setupAPI(t, "")
	_, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	for _, path := range []string{"/message/7/read", "/message/7/forward", "/chat/user:42/history?limit=5", "/group?title=x"} {
		method := "POST"
		if path == "/chat/user:42/history?limit=5" {
			method = "GET"
		}
		status, out := apiRequest(t, s, method, path, "one", nil)
		require.Equal(t, 400, status, out)
		require.Equal(t, "INVALID_REQUEST", out["code"])
	}
}

func TestProviderGETQueryPeerAndBooleanUseTypedNativeValues(t *testing.T) {
	type call struct {
		Operation string
		Payload   map[string]any
	}
	calls := make(chan call, 4)
	s, svc := setupAPIWithFactory(t, "", func(domains.Device) domains.Client {
		return &testClient{callFn: func(_ context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
			var p map[string]any
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			calls <- call{op, p}
			return json.RawMessage(`{}`), nil
		}}
	})
	_, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	for _, path := range []string{"/group/participants?peer=group:77&limit=12", "/user/my/groups?is_owner=true", "/group/invite-link?peer=group:77", "/user/info"} {
		r := httptest.NewRequest("GET", path, nil)
		r.SetBasicAuth("test", "password")
		r.Header.Set("X-Device-Id", "one")
		scopeTestRequest(t, s, r)
		res, err := s.App.Test(r)
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode, path)
		res.Body.Close()
	}
	first := <-calls
	require.Equal(t, "group.members", first.Operation)
	require.Equal(t, map[string]any{"type": "group", "id": "77"}, first.Payload["peer"])
	require.Equal(t, float64(12), first.Payload["limit"])
	require.Equal(t, true, (<-calls).Payload["is_owner"])
	require.Equal(t, "group.link", (<-calls).Operation)
	require.Equal(t, "account.info", (<-calls).Operation)
	for _, path := range []string{"/group/participants?peer=77", "/user/my/groups?is_owner=maybe"} {
		r := httptest.NewRequest("GET", path, nil)
		r.SetBasicAuth("test", "password")
		r.Header.Set("X-Device-Id", "one")
		scopeTestRequest(t, s, r)
		res, err := s.App.Test(r)
		require.NoError(t, err)
		require.Equal(t, 400, res.StatusCode, path)
		res.Body.Close()
	}
	require.Empty(t, calls)
}

func TestProviderGETWithoutBodyOverRealTCP(t *testing.T) {
	calls := make(chan string, 1)
	s, svc := setupAPIWithFactory(t, "", func(domains.Device) domains.Client {
		return &testClient{callFn: func(_ context.Context, op string, _ json.RawMessage) (json.RawMessage, error) {
			calls <- op
			return json.RawMessage(`{"messages":[]}`), nil
		}}
	})
	_, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- s.App.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() {
		require.NoError(t, s.App.ShutdownWithTimeout(time.Second))
		require.NoError(t, <-done)
	})
	r, err := http.NewRequest("GET", "http://"+listener.Addr().String()+"/chat/user:42/history?limit=6", nil)
	require.NoError(t, err)
	r.SetBasicAuth("test", "password")
	r.Header.Set("X-Device-Id", "one")
	scopeTestRequest(t, s, r)
	client := &http.Client{Timeout: time.Second}
	res, err := client.Do(r)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "chat.history", <-calls)
}

func TestReviewedGroupMutationsUseDurableRESTDispatch(t *testing.T) {
	calls := make(chan string, 2)
	s, svc := setupAPIWithFactory(t, "", func(domains.Device) domains.Client {
		return &testClient{callFn: func(_ context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
			var payload struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil || payload.RequestID == "" {
				return nil, domains.E("TEST_MISSING_REQUEST_ID", "durable ID missing", 500)
			}
			calls <- op
			return json.RawMessage(`{"acknowledged":true}`), nil
		}}
	})
	_, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	challenge, err := svc.StartAuth(context.Background(), "one", "+10000000000")
	require.NoError(t, err)
	_, err = svc.SubmitCode(context.Background(), "one", challenge.ID, "synthetic")
	require.NoError(t, err)
	for _, test := range []struct{ path, body, operation string }{
		{"/group/topic", `{"peer":{"type":"group","id":"77"},"description":""}`, "group.description"},
		{"/group/participants/remove", `{"peer":{"type":"group","id":"77"},"user":{"type":"user","id":"42"}}`, "group.remove"},
	} {
		r := httptest.NewRequest("POST", test.path, bytes.NewBufferString(test.body))
		r.SetBasicAuth("test", "password")
		r.Header.Set("X-Device-Id", "one")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", test.operation)
		scopeTestRequest(t, s, r)
		res, err := s.App.Test(r)
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode)
		var response struct {
			Results domains.Operation `json:"results"`
		}
		require.NoError(t, json.NewDecoder(res.Body).Decode(&response))
		res.Body.Close()
		require.Equal(t, "succeeded", response.Results.State)
		require.Equal(t, test.operation, <-calls)
		require.NotEmpty(t, response.Results.ID)
	}
}
