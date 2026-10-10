package rest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/mimalef70/goomni/src/usecase"
	"github.com/stretchr/testify/require"
)

// This fixture exercises the real HTTP, lifecycle and durable-work boundaries.
// Its three provider clients are synthetic. The A/B names are consumer-owned
// mappings, not a claim that GoBale authenticates tenants or enforces their ACLs.
type consumerFlowClient struct {
	testClient
	alias, account string
	passwordNeeded bool
	sink           domains.Sink
	sends          atomic.Int32
}

func (c *consumerFlowClient) StartAuth(context.Context, string) (domains.Challenge, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Auth = "awaiting_code"
	return domains.Challenge{ID: "challenge-" + c.alias, State: "awaiting_code", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (c *consumerFlowClient) SubmitCode(_ context.Context, challenge, code string) (*domains.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if challenge != "challenge-"+c.alias || code != "code-"+c.alias {
		return nil, domains.E("INVALID_CODE", "synthetic invalid code", 400)
	}
	if c.passwordNeeded {
		c.status.Auth = "awaiting_password"
		return nil, nil
	}
	return c.session(), nil
}

func (c *consumerFlowClient) SubmitPassword(_ context.Context, challenge, password string) (*domains.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if challenge != "challenge-"+c.alias || password != "password-"+c.alias || c.status.Auth != "awaiting_password" {
		return nil, domains.E("INVALID_PASSWORD", "synthetic invalid password", 400)
	}
	return c.session(), nil
}

func (c *consumerFlowClient) session() *domains.Session {
	return &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: c.account, Token: "synthetic-token-" + c.alias, DeviceHash: "synthetic-hash-" + c.alias}
}

func (c *consumerFlowClient) Connect(_ context.Context, _ *domains.Session, sink domains.Sink) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink = sink
	c.status = domains.ConnectionStatus{Auth: "authenticated", Transport: "connected", Recovery: "degraded"}
	return nil
}

func (c *consumerFlowClient) Send(_ context.Context, req domains.SendRequest) (domains.SendResult, error) {
	c.sends.Add(1)
	if req.Text == "synthetic uncertain send" {
		return domains.SendResult{}, &domains.Error{Code: "SEND_UNKNOWN", Message: "synthetic uncertainty", HTTP: 504, Ambiguous: true}
	}
	return domains.SendResult{MessageID: req.RequestID, Date: time.Now().UTC()}, nil
}

func (c *consumerFlowClient) emit(ctx context.Context, event domains.Event) error {
	c.mu.Lock()
	sink := c.sink
	c.mu.Unlock()
	return sink(ctx, event)
}

// Unlike apiRequest, this never looks up a fresh device instance on the caller's
// behalf. References below are the exact values returned during provisioning.
func consumerFlowRequest(t *testing.T, srv *Server, method, path string, ref *domains.Device, body any, key string) (int, map[string]any) {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	response := consumerFlowRaw(t, srv, method, path, ref, data, "application/json", key)
	defer response.Body.Close()
	var out map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&out))
	return response.StatusCode, out
}

func consumerFlowRaw(t *testing.T, srv *Server, method, path string, ref *domains.Device, data []byte, contentType, key string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.SetBasicAuth("test", "password")
	req.Header.Set("Content-Type", contentType)
	if ref != nil {
		req.Header.Set("X-Device-Id", ref.ID)
		req.Header.Set("X-Device-Instance", ref.InstanceID)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := srv.App.Test(req)
	require.NoError(t, err)
	return response
}

func consumerFlowDevice(t *testing.T, body map[string]any) domains.Device {
	t.Helper()
	raw, err := json.Marshal(body["results"])
	require.NoError(t, err)
	var d domains.Device
	require.NoError(t, json.Unmarshal(raw, &d))
	require.NotEmpty(t, d.ID)
	require.Len(t, d.InstanceID, 64)
	return d
}

func TestConsumerConnectionFlow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "consumer.db")
	mediaRoot := filepath.Join(root, "media")
	accounts := map[string]string{"a-primary": "101", "a-secondary": "102", "b-primary": "103"}
	var clientsMu sync.Mutex
	clients := map[string]*consumerFlowClient{}
	factory := func(d domains.Device) domains.Client {
		c := &consumerFlowClient{alias: d.ID, account: accounts[d.ID], passwordNeeded: d.ID == "a-secondary"}
		c.status.Auth = "auth_required"
		clientsMu.Lock()
		clients[d.ID] = c
		clientsMu.Unlock()
		return c
	}
	clientFor := func(alias string) *consumerFlowClient {
		clientsMu.Lock()
		defer clientsMu.Unlock()
		return clients[alias]
	}
	var srv *Server
	var svc *usecase.Service
	var st *storage.Store
	start := func() {
		var err error
		st, err = storage.Open(dbPath, bytes.Repeat([]byte{19}, 32))
		require.NoError(t, err)
		svc = usecase.New(st, usecase.Options{PollInterval: 5 * time.Millisecond}, factory)
		require.NoError(t, svc.Start(ctx))
		srv, err = New(svc, st, Options{BasicAuth: "test:password", MediaRoot: mediaRoot, MaxMediaBytes: 1024, SendWait: 500 * time.Millisecond})
		require.NoError(t, err)
	}
	stop := func() {
		shutdown, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		require.NoError(t, svc.Close(shutdown))
		if srv.opts.MediaManager != nil {
			require.NoError(t, srv.opts.MediaManager.Close())
		}
		require.NoError(t, st.Close())
	}
	start()
	t.Cleanup(stop)

	status, body := consumerFlowRequest(t, srv, "POST", "/devices", nil, map[string]string{"provider": "bale", "device_id": "missing-key"}, "")
	require.Equal(t, 400, status, body)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", body["code"])
	status, body = consumerFlowRequest(t, srv, "POST", "/devices", nil, map[string]string{"provider": "bale", "device_id": "invalid-atomic", "webhook_url": "https://example.invalid/events"}, "invalid-atomic")
	require.Equal(t, 400, status, body)
	status, body = consumerFlowRequest(t, srv, "GET", "/devices", nil, nil, "")
	require.Equal(t, 200, status)
	require.Empty(t, body["results"], "failed provisioning must not leave an unconfigured device")

	refs := map[string]domains.Device{}
	for _, alias := range []string{"a-primary", "a-secondary", "b-primary"} {
		request := map[string]any{"provider": "bale", "device_id": alias}
		status, body = consumerFlowRequest(t, srv, "POST", "/devices", nil, request, "provision-"+alias)
		require.Equal(t, 201, status, body)
		refs[alias] = consumerFlowDevice(t, body)
		status, body = consumerFlowRequest(t, srv, "POST", "/devices", nil, request, "provision-"+alias)
		require.Equal(t, 200, status, body)
		require.Equal(t, refs[alias].InstanceID, consumerFlowDevice(t, body).InstanceID)
	}
	a, a2, b := refs["a-primary"], refs["a-secondary"], refs["b-primary"]
	status, body = consumerFlowRequest(t, srv, "POST", "/devices", nil, map[string]string{"provider": "bale", "device_id": "different"}, "provision-a-primary")
	require.Equal(t, 409, status, body)
	require.Equal(t, "IDEMPOTENCY_CONFLICT", body["code"])

	for _, ref := range []domains.Device{a, a2, b} {
		status, body = consumerFlowRequest(t, srv, "POST", "/devices/"+ref.ID+"/login", &ref, map[string]string{"phone": "+10000000000"}, "")
		require.Equal(t, 200, status, body)
		require.Equal(t, "challenge-"+ref.ID, body["results"].(map[string]any)["challenge_id"])
	}
	status, body = consumerFlowRequest(t, srv, "POST", "/devices/"+b.ID+"/login/code", &b, map[string]string{"challenge_id": "challenge-" + a.ID, "code": "code-" + a.ID}, "")
	require.Equal(t, 400, status, body)
	require.Equal(t, "CHALLENGE_EXPIRED", body["code"])
	for _, ref := range []domains.Device{a, a2, b} {
		status, body = consumerFlowRequest(t, srv, "GET", "/devices/"+ref.ID+"/login", &ref, nil, "")
		require.Equal(t, 200, status, body)
		require.Equal(t, "challenge-"+ref.ID, body["results"].(map[string]any)["challenge"].(map[string]any)["challenge_id"])
		status, body = consumerFlowRequest(t, srv, "POST", "/devices/"+ref.ID+"/login/code", &ref, map[string]string{"challenge_id": "challenge-" + ref.ID, "code": "code-" + ref.ID}, "")
		require.Equal(t, 200, status, body)
		if ref.ID == a2.ID {
			require.Equal(t, "awaiting_password", body["results"].(map[string]any)["auth"])
			status, body = consumerFlowRequest(t, srv, "POST", "/devices/"+ref.ID+"/login/password", &ref, map[string]string{"challenge_id": "challenge-" + ref.ID, "password": "password-" + ref.ID}, "")
			require.Equal(t, 200, status, body)
		}
		require.Equal(t, "authenticated", body["results"].(map[string]any)["auth"])
		status, body = consumerFlowRequest(t, srv, "GET", "/devices/"+ref.ID, &ref, nil, "")
		require.Equal(t, 200, status, body)
		require.Equal(t, accounts[ref.ID], consumerFlowDevice(t, body).AccountID)
	}

	for _, test := range []struct {
		path string
		ref  *domains.Device
		code string
	}{
		{"/app/status", nil, "DEVICE_ID_REQUIRED"},
		{"/app/status", &domains.Device{ID: a.ID}, "DEVICE_INSTANCE_REQUIRED"},
		{"/devices/" + a.ID + "/status", &b, "DEVICE_SELECTOR_CONFLICT"},
		{"/app/status?device_id=" + b.ID, &a, "DEVICE_SELECTOR_CONFLICT"},
	} {
		status, body = consumerFlowRequest(t, srv, "GET", test.path, test.ref, nil, "")
		require.Equal(t, 400, status, body)
		require.Equal(t, test.code, body["code"])
	}
	wrongInstance := a
	wrongInstance.InstanceID = b.InstanceID
	status, body = consumerFlowRequest(t, srv, "GET", "/app/status", &wrongInstance, nil, "")
	require.Equal(t, 409, status, body)
	require.Equal(t, "DEVICE_INSTANCE_CHANGED", body["code"])

	send := map[string]any{"peer": domains.Peer{Type: "user", ID: "42"}, "message": "synthetic send"}
	status, body = consumerFlowRequest(t, srv, "POST", "/send/message", &a, send, "")
	require.Equal(t, 400, status, body)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", body["code"])
	require.Zero(t, clientFor(a.ID).sends.Load())
	var opID string
	for i := 0; i < 2; i++ {
		status, body = consumerFlowRequest(t, srv, "POST", "/send/message", &a, send, "persisted-message-key")
		require.Equal(t, 200, status, body)
		op := body["results"].(map[string]any)
		require.Equal(t, "succeeded", op["state"])
		if i == 0 {
			opID = op["send_id"].(string)
		} else {
			require.Equal(t, opID, op["send_id"])
		}
	}
	require.EqualValues(t, 1, clientFor(a.ID).sends.Load())
	status, body = consumerFlowRequest(t, srv, "POST", "/send/message", &b, send, "persisted-message-key")
	require.Equal(t, 200, status, body)
	require.NotEqual(t, opID, body["results"].(map[string]any)["send_id"], "send keys belong to the immutable connection")
	send["message"] = "changed content"
	status, body = consumerFlowRequest(t, srv, "POST", "/send/message", &a, send, "persisted-message-key")
	require.Equal(t, 409, status, body)
	send["message"] = "synthetic uncertain send"
	var unknownID string
	for i := 0; i < 2; i++ {
		status, body = consumerFlowRequest(t, srv, "POST", "/send/message", &a2, send, "persisted-unknown-key")
		require.Equal(t, 202, status, body)
		require.Equal(t, "SEND_UNKNOWN", body["code"])
		op := body["results"].(map[string]any)
		require.Equal(t, "unknown", op["state"])
		if i == 0 {
			unknownID = op["send_id"].(string)
		} else {
			require.Equal(t, unknownID, op["send_id"])
		}
	}
	require.EqualValues(t, 1, clientFor(a2.ID).sends.Load(), "an uncertain send must never be blindly repeated")

	mediaBytes := []byte("synthetic bounded media")
	response := consumerFlowRaw(t, srv, "POST", "/media", &a, mediaBytes, "application/octet-stream", "")
	require.Equal(t, 201, response.StatusCode)
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.NoError(t, response.Body.Close())
	mediaID := body["results"].(map[string]any)["id"].(string)
	response = consumerFlowRaw(t, srv, "GET", "/media/"+mediaID, &a, nil, "", "")
	require.Equal(t, 200, response.StatusCode)
	download, err := io.ReadAll(io.LimitReader(response.Body, 1025))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, mediaBytes, download)

	schedule := map[string]any{"peer": domains.Peer{Type: "user", ID: "42"}, "message": "synthetic future send", "scheduled_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "timezone": "UTC"}
	status, body = consumerFlowRequest(t, srv, "POST", "/send/schedules", &a, schedule, "")
	require.Equal(t, 400, status, body)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", body["code"])
	status, body = consumerFlowRequest(t, srv, "POST", "/send/schedules", &a, schedule, "persisted-schedule-key")
	require.Equal(t, 200, status, body)
	scheduleID := body["results"].(map[string]any)["id"].(string)
	for _, other := range []domains.Device{a2, b} {
		for _, path := range []string{"/media/" + mediaID, "/send/operations/" + opID, "/send/schedules/" + scheduleID, "/send/schedules/" + scheduleID + "/occurrences"} {
			status, body = consumerFlowRequest(t, srv, "GET", path, &other, nil, "")
			require.Equal(t, 404, status, "%s: %v", path, body)
		}
		status, body = consumerFlowRequest(t, srv, "POST", "/send/schedules/"+scheduleID+"/cancel", &other, nil, "")
		require.Equal(t, 404, status, body)
	}
	status, body = consumerFlowRequest(t, srv, "GET", "/send/schedules/"+scheduleID, &a, nil, "")
	require.Equal(t, 200, status, body)
	require.Equal(t, "active", body["results"].(map[string]any)["status"])

	type capturedDelivery struct {
		body       []byte
		event      domains.Event
		deliveryID string
		valid      bool
	}
	var hookMu sync.Mutex
	var captured []capturedDelivery
	secret := "synthetic-consumer-webhook-secret"
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(raw)
		var event domains.Event
		decodeErr := json.Unmarshal(raw, &event)
		valid := readErr == nil && decodeErr == nil && hmac.Equal([]byte(r.Header.Get("X-Hub-Signature-256")), []byte("sha256="+hex.EncodeToString(mac.Sum(nil)))) && r.Header.Get("X-GoOmni-Event-Id") == event.ID
		hookMu.Lock()
		captured = append(captured, capturedDelivery{body: raw, event: event, deliveryID: r.Header.Get("X-GoOmni-Delivery-Id"), valid: valid})
		count := len(captured)
		hookMu.Unlock()
		if count == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer hook.Close()
	status, body = consumerFlowRequest(t, srv, "PATCH", "/devices/"+a.ID+"/webhook", &a, map[string]string{"webhook_url": hook.URL, "webhook_secret": secret}, "")
	require.Equal(t, 200, status, body)
	event := domains.Event{ID: "synthetic-event", Type: "message", AccountID: accounts[a.ID], InstanceID: "untrusted-provider-instance", Peer: domains.Peer{Type: "user", ID: "42"}, MessageID: "-700", Direction: "incoming", Time: time.Now().UTC(), Payload: json.RawMessage(`{"kind":"text","message":"synthetic inbound"}`)}
	require.NoError(t, clientFor(a.ID).emit(ctx, event))
	require.NoError(t, clientFor(a.ID).emit(ctx, event))
	var deliveryID, publicEventID string
	require.Eventually(t, func() bool {
		status, body = consumerFlowRequest(t, srv, "GET", "/deliveries", &a, nil, "")
		if status != 200 {
			return false
		}
		rows := body["results"].([]any)
		if len(rows) != 1 {
			return false
		}
		delivery := rows[0].(map[string]any)
		deliveryID = delivery["delivery_id"].(string)
		publicEventID = delivery["event_id"].(string)
		return delivery["state"] == "retry" && delivery["attempts"] == float64(1)
	}, 3*time.Second, 5*time.Millisecond)
	status, body = consumerFlowRequest(t, srv, "GET", "/deliveries/"+deliveryID, &b, nil, "")
	require.Equal(t, 404, status, body)
	stop()
	start() // Reopen SQLite and restore sessions; the retry was not an in-memory task.
	require.Eventually(t, func() bool {
		status, body = consumerFlowRequest(t, srv, "GET", "/deliveries/"+deliveryID, &a, nil, "")
		return status == 200 && body["results"].(map[string]any)["state"] == "delivered"
	}, 4*time.Second, 10*time.Millisecond)
	hookMu.Lock()
	observed := append([]capturedDelivery(nil), captured...)
	hookMu.Unlock()
	require.Len(t, observed, 2)
	require.Equal(t, observed[0].body, observed[1].body)
	for _, delivery := range observed {
		require.True(t, delivery.valid)
		require.Equal(t, deliveryID, delivery.deliveryID)
		require.Equal(t, publicEventID, delivery.event.ID)
		require.Equal(t, a.ID, delivery.event.SessionID)
		require.Equal(t, a.InstanceID, delivery.event.InstanceID)
		require.Equal(t, accounts[a.ID], delivery.event.AccountID)
	}
	status, body = consumerFlowRequest(t, srv, "GET", "/send/operations/"+unknownID, &a2, nil, "")
	require.Equal(t, 200, status, body)
	require.Equal(t, "unknown", body["results"].(map[string]any)["state"])
	require.Zero(t, clientFor(a2.ID).sends.Load(), "restart must not retry uncertain provider writes")

	status, body = consumerFlowRequest(t, srv, "DELETE", "/devices/"+b.ID, &b, nil, "")
	require.Equal(t, 200, status, body)
	status, body = consumerFlowRequest(t, srv, "POST", "/devices", nil, map[string]string{"provider": "bale", "device_id": b.ID}, "provision-b-primary")
	require.Equal(t, 409, status, body)
	require.Equal(t, "PROVISIONING_RETIRED", body["code"])
	status, body = consumerFlowRequest(t, srv, "POST", "/devices", nil, map[string]string{"provider": "bale", "device_id": b.ID}, "provision-b-replacement")
	require.Equal(t, 201, status, body)
	replacement := consumerFlowDevice(t, body)
	require.NotEqual(t, b.InstanceID, replacement.InstanceID)
	status, body = consumerFlowRequest(t, srv, "GET", "/devices/"+b.ID+"/status", &b, nil, "")
	require.Equal(t, 409, status, body)
	require.Equal(t, "DEVICE_INSTANCE_CHANGED", body["code"])
}
