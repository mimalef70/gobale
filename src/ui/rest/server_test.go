package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/mimalef70/gobale/src/usecase"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// This compares the running router to the published contract, including routes
// installed by loops; a source-only generator cannot prove that parity.
func TestOpenAPIRouteParity(t *testing.T) {
	s, _ := setupAPI(t, "")
	data, err := os.ReadFile("../../../docs/openapi.yaml")
	require.NoError(t, err)
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(data, &spec))
	actual, documented := map[string]bool{}, map[string]bool{}
	parameter := regexp.MustCompile(`:([a-z_]+)`)
	for _, route := range s.App.GetRoutes(true) {
		if route.Method == "HEAD" {
			continue
		}
		path := parameter.ReplaceAllString(route.Path, "{$1}")
		actual[route.Method+" "+path] = true
	}
	for path, methods := range spec.Paths {
		if strings.HasPrefix(path, "/ui/") {
			continue
		}
		for method := range methods {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}
	require.Equal(t, documented, actual)
}

// Required response fields must exist in the actual public response, including
// nested objects. This catches drift that route parity alone cannot detect.
func TestOpenAPIAppInfoRequiredFields(t *testing.T) {
	s, _ := setupAPI(t, "")
	data, err := os.ReadFile("../../../docs/openapi.yaml")
	require.NoError(t, err)
	var spec struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	require.NoError(t, yaml.Unmarshal(data, &spec))
	status, body := apiRequest(t, s, "GET", "/app/info", "", nil)
	require.Equal(t, 200, status)
	var validate func(map[string]any, map[string]any)
	validate = func(schema, value map[string]any) {
		required, ok := schema["required"].([]any)
		require.True(t, ok, "object must document its required fields")
		properties := schema["properties"].(map[string]any)
		require.Len(t, properties, len(value), "app info response fields changed; update the contract")
		for _, item := range required {
			key := item.(string)
			field, exists := value[key]
			require.True(t, exists, "required response field %s missing", key)
			property := properties[key].(map[string]any)
			if property["type"] == "object" {
				object, ok := field.(map[string]any)
				require.True(t, ok, "response field %s must be an object", key)
				validate(property, object)
			}
		}
	}
	validate(spec.Components.Schemas["AppInfo"], body["results"].(map[string]any))
}

type testClient struct {
	mu     sync.Mutex
	status domains.ConnectionStatus
	callFn func(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

func (f *testClient) StartAuth(context.Context, string) (domains.Challenge, error) {
	return domains.Challenge{ID: "challenge", State: "awaiting_code"}, nil
}
func (f *testClient) SubmitCode(context.Context, string, string) (*domains.Session, error) {
	return &domains.Session{UserID: "9007199254740993", Token: "local-test-token", DeviceHash: "local-test-hash"}, nil
}
func (f *testClient) SubmitPassword(ctx context.Context, a, b string) (*domains.Session, error) {
	return f.SubmitCode(ctx, a, b)
}
func (f *testClient) Connect(context.Context, *domains.Session, domains.Sink) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = domains.ConnectionStatus{Auth: "authenticated", Transport: "connected", Recovery: "degraded"}
	return nil
}
func (f *testClient) Disconnect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.Transport = "disconnected"
	return nil
}
func (f *testClient) Logout(c context.Context) error { return f.Disconnect(c) }
func (f *testClient) Status() domains.ConnectionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}
func (f *testClient) Send(ctx context.Context, r domains.SendRequest) (domains.SendResult, error) {
	return domains.SendResult{MessageID: r.RequestID, Date: time.Now().UTC()}, nil
}
func (f *testClient) Call(ctx context.Context, operation string, body json.RawMessage) (json.RawMessage, error) {
	if f.callFn != nil {
		return f.callFn(ctx, operation, body)
	}
	return nil, domains.Unsupported("fixture operation")
}
func setupAPI(t *testing.T, base string) (*Server, *usecase.Service) {
	return setupAPIWithFactory(t, base, func(domains.Device) domains.Client { return &testClient{} })
}
func setupAPIWithFactory(t *testing.T, base string, factory domains.ClientFactory) (*Server, *usecase.Service) {
	t.Helper()
	dir := t.TempDir()
	st, e := storage.Open(filepath.Join(dir, "test.db"), bytes.Repeat([]byte{7}, 32))
	require.NoError(t, e)
	svc := usecase.New(st, usecase.Options{PollInterval: 5 * time.Millisecond}, factory)
	require.NoError(t, svc.Start(context.Background()))
	srv, e := New(svc, st, Options{BasicAuth: "test:password", BasePath: base, MediaRoot: filepath.Join(dir, "media"), MaxMediaBytes: 1024, SendWait: time.Second})
	require.NoError(t, e)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, svc.Close(ctx))
		require.NoError(t, st.Close())
	})
	return srv, svc
}
func apiRequest(t *testing.T, s *Server, method, path, device string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.SetBasicAuth("test", "password")
	r.Header.Set("Content-Type", "application/json")
	if device != "" {
		r.Header.Set("X-Device-Id", device)
	}
	res, e := s.App.Test(r)
	require.NoError(t, e)
	defer res.Body.Close()
	var out map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&out))
	return res.StatusCode, out
}
func TestAuthBasePathAndExplicitDeviceIsolation(t *testing.T) {
	s, svc := setupAPI(t, "/api")
	res, e := s.App.Test(httptest.NewRequest("GET", "/api/devices", nil))
	require.NoError(t, e)
	require.Equal(t, 401, res.StatusCode)
	res.Body.Close()
	res, e = s.App.Test(httptest.NewRequest("GET", "/api/health", nil))
	require.NoError(t, e)
	require.Equal(t, 200, res.StatusCode)
	res.Body.Close()
	_, e = svc.CreateDevice(context.Background(), "one")
	require.NoError(t, e)
	status, out := apiRequest(t, s, "GET", "/api/app/status", "missing", nil)
	require.Equal(t, 404, status, out)
	status, _ = apiRequest(t, s, "GET", "/api/app/status", "", nil)
	require.Equal(t, 200, status)
	_, e = svc.CreateDevice(context.Background(), "two")
	require.NoError(t, e)
	status, _ = apiRequest(t, s, "GET", "/api/app/status", "", nil)
	require.Equal(t, 400, status)
}
func TestWebhookSecretIsWriteOnly(t *testing.T) {
	s, _ := setupAPI(t, "")
	status, _ := apiRequest(t, s, "POST", "/devices", "", map[string]any{"device_id": "one"})
	require.Equal(t, 201, status)
	status, out := apiRequest(t, s, "PATCH", "/devices/one/webhook", "", map[string]any{"webhook_url": "http://127.0.0.1:9999/hook", "webhook_secret": "super-sensitive"})
	require.Equal(t, 200, status, out)
	b, _ := json.Marshal(out)
	require.NotContains(t, string(b), "super-sensitive")
	status, out = apiRequest(t, s, "GET", "/devices/one/webhook", "", nil)
	require.Equal(t, 200, status)
	b, _ = json.Marshal(out)
	require.NotContains(t, string(b), "super-sensitive")
}
func TestAuthenticatedSendReturnsPersistedOperation(t *testing.T) {
	s, _ := setupAPI(t, "")
	_, _ = apiRequest(t, s, "POST", "/devices", "", map[string]any{"device_id": "one"})
	_, _ = apiRequest(t, s, "POST", "/devices/one/login", "", map[string]any{"phone": "+10000000000"})
	status, out := apiRequest(t, s, "POST", "/devices/one/login/code", "", map[string]any{"challenge_id": "challenge", "code": "fake"})
	require.Equal(t, 200, status, out)
	status, out = apiRequest(t, s, "POST", "/send/message", "one", map[string]any{"peer": map[string]string{"type": "user", "id": "9007199254740993"}, "message": "hello"})
	require.Equal(t, 200, status, out)
	op := out["results"].(map[string]any)
	require.NotEmpty(t, op["send_id"])
	require.Contains(t, []string{"sent", "succeeded"}, op["state"])
	_, e := s.service.CreateDevice(context.Background(), "other")
	require.NoError(t, e)
	status, _ = apiRequest(t, s, "GET", "/send/operations/"+op["send_id"].(string), "other", nil)
	require.Equal(t, 404, status)
}
func TestMediaScopeStreamingLimitAndSSRF(t *testing.T) {
	s, svc := setupAPI(t, "")
	_, e := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, e)
	_, e = svc.CreateDevice(context.Background(), "two")
	require.NoError(t, e)
	upload := func(n int) (int, map[string]any) {
		r := httptest.NewRequest("POST", "/media", bytes.NewReader(bytes.Repeat([]byte{42}, n)))
		r.SetBasicAuth("test", "password")
		r.Header.Set("X-Device-Id", "one")
		r.Header.Set("X-Filename", "../../x.txt")
		res, e := s.App.Test(r)
		require.NoError(t, e)
		defer res.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(res.Body).Decode(&out))
		return res.StatusCode, out
	}
	status, out := upload(100)
	require.Equal(t, 201, status, out)
	m := out["results"].(map[string]any)
	require.Equal(t, "x.txt", m["name"])
	status, _ = apiRequest(t, s, "GET", "/media/"+m["id"].(string), "two", nil)
	require.Equal(t, 404, status)
	status, _ = upload(1025)
	require.Equal(t, 413, status)
	status, out = apiRequest(t, s, "POST", "/media/fetch", "one", map[string]string{"url": "http://127.0.0.1:65500/secrets"})
	require.Equal(t, 400, status, out)
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "fc00::1"} {
		require.False(t, allowedMediaIP(net.ParseIP(ip)), ip)
	}
	require.True(t, allowedMediaIP(net.ParseIP("8.8.8.8")))
}
func TestUnsupportedProviderIsExplicit(t *testing.T) {
	s, svc := setupAPI(t, "")
	_, e := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, e)
	status, out := apiRequest(t, s, "POST", "/message/7/revoke", "one", map[string]string{})
	require.Contains(t, []int{501, 409}, status, out)
}
