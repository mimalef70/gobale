package rest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/goomni/src/config"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/mimalef70/goomni/src/ui/web"
	"github.com/mimalef70/goomni/src/usecase"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func integrationUIAssets(t *testing.T) *web.Bundle {
	t.Helper()
	// A validated synthetic base element keeps Go tests independent of Node.
	files := fstest.MapFS{"index.html": {Data: []byte(`<html><head><base href="__GOOMNI_UI_BASE__/"></head><body>synthetic UI</body></html>`)}}
	inventory := make(map[string]string)
	for name, file := range files {
		sum := sha256.Sum256(file.Data)
		inventory[name] = hex.EncodeToString(sum[:])
	}
	manifest, err := json.Marshal(web.Manifest{SchemaVersion: 1, Version: config.AppVersion, OpenAPISHA256: config.ContractSHA256, Files: inventory})
	require.NoError(t, err)
	files["build-manifest.json"] = &fstest.MapFile{Data: manifest}
	bundle, err := web.ValidateFS(files, config.AppVersion, config.ContractSHA256)
	require.NoError(t, err)
	return bundle
}

type browserIntegrationClient struct {
	*testClient
	starts   atomic.Int32
	logouts  atomic.Int32
	cooldown int64
}

func (c *browserIntegrationClient) StartAuth(context.Context, string) (domains.Challenge, error) {
	n := c.starts.Add(1)
	c.mu.Lock()
	c.status.Auth = "awaiting_code"
	c.mu.Unlock()
	return domains.Challenge{ID: fmt.Sprintf("synthetic-challenge-%d", n), State: "awaiting_code", ExpiresAt: time.Now().Add(2 * time.Minute), ResendAfterSeconds: &c.cooldown}, nil
}
func (c *browserIntegrationClient) Logout(ctx context.Context) error {
	c.logouts.Add(1)
	return c.testClient.Logout(ctx)
}

func integrationBrowserServer(t *testing.T, base string, enabled bool, factory domains.ClientFactory) (*Server, *usecase.Service, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := storage.Open(filepath.Join(dir, "browser.db"), bytes.Repeat([]byte{8}, 32))
	require.NoError(t, err)
	if factory == nil {
		factory = func(domains.Device) domains.Client {
			return &browserIntegrationClient{testClient: &testClient{}, cooldown: 60}
		}
	}
	svc := usecase.New(st, usecase.Options{}, factory)
	srv, err := New(svc, st, Options{UIEnabled: enabled, UIAssets: integrationUIAssets(t), BasicAuth: "admin:synthetic-ui-password", Version: config.AppVersion, BasePath: base, MediaRoot: filepath.Join(dir, "media")})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, svc.Close(ctx))
		require.NoError(t, st.Close())
	})
	return srv, svc, st
}

type browserIntegrationSession struct {
	cookie *http.Cookie
	csrf   string
}

func browserIntegrationRequest(t *testing.T, s *Server, session browserIntegrationSession, method, path string, body any, headers map[string]string, basic bool) (*http.Response, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:3017"+s.opts.BasePath+path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:3017")
	if session.cookie != nil {
		r.AddCookie(session.cookie)
		r.Header.Set("X-CSRF-Token", session.csrf)
	}
	if basic {
		r.SetBasicAuth("admin", "synthetic-ui-password")
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	response, err := s.App.Test(r)
	require.NoError(t, err)
	b, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	response.Body.Close()
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out), "%s %s returned %s", method, path, string(b))
	return response, out
}
func integrationLogin(t *testing.T, s *Server) browserIntegrationSession {
	t.Helper()
	res, out := browserIntegrationRequest(t, s, browserIntegrationSession{}, "POST", "/ui/auth/session", map[string]string{"username": "admin", "password": "synthetic-ui-password"}, nil, false)
	require.Equal(t, 200, res.StatusCode, out)
	require.Len(t, res.Cookies(), 1)
	return browserIntegrationSession{cookie: res.Cookies()[0], csrf: out["results"].(map[string]any)["csrf_token"].(string)}
}
func browserInstance(d domains.Device) map[string]string {
	return map[string]string{"X-Device-Id": d.ID, "X-Device-Instance": d.InstanceToken()}
}

func TestBrowserIntegrationAllowlistAndCredentialIsolation(t *testing.T) {
	srv, _, _ := integrationBrowserServer(t, "", true, nil)
	session := integrationLogin(t, srv)
	for _, path := range []string{"/ui/api/devices", "/ui/api/devices/overview", "/ui/api/app/info", "/ui/api/ready"} {
		res, out := browserIntegrationRequest(t, srv, session, "GET", path, nil, nil, false)
		require.Equal(t, 200, res.StatusCode, out)
		res, out = browserIntegrationRequest(t, srv, browserIntegrationSession{}, "GET", path, nil, nil, true)
		require.Equal(t, 401, res.StatusCode, out)
		require.Equal(t, "UI_UNAUTHORIZED", out["code"])
	}
	res, out := browserIntegrationRequest(t, srv, session, "GET", "/devices", nil, nil, false)
	require.Equal(t, 401, res.StatusCode, out)
	res, out = browserIntegrationRequest(t, srv, browserIntegrationSession{}, "GET", "/devices", nil, nil, true)
	require.Equal(t, 200, res.StatusCode, out)
	for _, test := range []struct{ method, path string }{{"POST", "/ui/api/send/message"}, {"GET", "/ui/api/chats"}, {"POST", "/ui/api/operations/account.name"}, {"POST", "/ui/api/media"}, {"GET", "/ui/api/user/avatar"}, {"GET", "/ui/api/metrics"}} {
		res, out := browserIntegrationRequest(t, srv, session, test.method, test.path, map[string]any{}, nil, false)
		require.Equal(t, 404, res.StatusCode, out)
		require.Equal(t, "NOT_FOUND", out["code"])
	}
	res, out = browserIntegrationRequest(t, srv, session, "DELETE", "/ui/auth/session", nil, nil, false)
	require.Equal(t, 200, res.StatusCode, out)
	res, out = browserIntegrationRequest(t, srv, session, "GET", "/ui/api/devices", nil, nil, false)
	require.Equal(t, 401, res.StatusCode, out)
}

func TestBrowserIntegrationInstanceGuardRejectsReplacedAlias(t *testing.T) {
	f := &browserIntegrationClient{testClient: &testClient{}}
	srv, svc, _ := integrationBrowserServer(t, "", true, func(domains.Device) domains.Client { return f })
	ctx := context.Background()
	original, err := svc.CreateDevice(ctx, "same", domains.ProviderBale)
	require.NoError(t, err)
	session := integrationLogin(t, srv)
	for _, test := range []struct{ method, path string }{{"GET", "/ui/api/devices/same/status"}, {"PATCH", "/ui/api/devices/same/webhook"}, {"POST", "/ui/api/devices/same/logout"}, {"GET", "/ui/api/deliveries"}} {
		res, out := browserIntegrationRequest(t, srv, session, test.method, test.path, map[string]any{}, map[string]string{"X-Device-Id": "same"}, false)
		require.Equal(t, 400, res.StatusCode, out)
		require.Equal(t, "DEVICE_INSTANCE_REQUIRED", out["code"])
	}
	require.NoError(t, svc.DeleteDevice(ctx, original.ID))
	replacement, err := svc.CreateDevice(ctx, original.ID, domains.ProviderBale)
	require.NoError(t, err)
	for _, test := range []struct{ method, path string }{{"DELETE", "/ui/api/devices/same"}, {"POST", "/ui/api/devices/same/logout"}, {"POST", "/ui/api/devices/same/login"}, {"PATCH", "/ui/api/devices/same/webhook"}, {"POST", "/ui/api/deliveries/anything/retry"}} {
		res, out := browserIntegrationRequest(t, srv, session, test.method, test.path, map[string]string{"phone": "+15550000123", "webhook_url": "https://new.test/hook"}, browserInstance(original), false)
		require.Equal(t, 409, res.StatusCode, out)
		require.Equal(t, "DEVICE_INSTANCE_CHANGED", out["code"])
	}
	require.Zero(t, f.logouts.Load())
	require.Zero(t, f.starts.Load())
	current, err := svc.GetDevice(ctx, replacement.ID)
	require.NoError(t, err)
	require.Equal(t, replacement.ConnectionID, current.ConnectionID)
	require.Empty(t, current.Webhook.URL)
	// Machine and browser APIs both require the same immutable precondition.
	res, out := browserIntegrationRequest(t, srv, browserIntegrationSession{}, "GET", "/devices/same", nil, browserInstance(original), true)
	require.Equal(t, 409, res.StatusCode, out)
	res, out = browserIntegrationRequest(t, srv, browserIntegrationSession{}, "GET", "/devices/same", nil, nil, true)
	require.Equal(t, 400, res.StatusCode, out)
	require.Equal(t, "DEVICE_INSTANCE_REQUIRED", out["code"])
}

func TestBrowserIntegrationFiftyAccountSnapshotIsLocal(t *testing.T) {
	var factories atomic.Int32
	srv, svc, _ := integrationBrowserServer(t, "", true, func(domains.Device) domains.Client {
		factories.Add(1)
		return &testClient{callFn: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			t.Error("snapshot called provider")
			return nil, nil
		}}
	})
	for n := range 50 {
		_, err := svc.CreateDevice(context.Background(), fmt.Sprintf("account-%02d", n), domains.ProviderBale)
		require.NoError(t, err)
	}
	session := integrationLogin(t, srv)
	res, out := browserIntegrationRequest(t, srv, session, "GET", "/ui/api/devices/overview", nil, nil, false)
	require.Equal(t, 200, res.StatusCode, out)
	result := out["results"].(map[string]any)
	require.NotEmpty(t, result["server_time"])
	require.Len(t, result["devices"], 50)
	require.Zero(t, factories.Load())
}

func TestBrowserIntegrationLoginRefreshCooldownAndWebhookPreview(t *testing.T) {
	srv, svc, st := integrationBrowserServer(t, "", true, nil)
	ctx := context.Background()
	d, err := svc.CreateDevice(ctx, "login", domains.ProviderBale)
	require.NoError(t, err)
	session := integrationLogin(t, srv)
	headers := browserInstance(d)
	res, out := browserIntegrationRequest(t, srv, session, "POST", "/ui/api/devices/login/login", map[string]string{"phone": "+15550000123"}, headers, false)
	require.Equal(t, 200, res.StatusCode, out)
	challenge := out["results"].(map[string]any)["challenge_id"]
	res, out = browserIntegrationRequest(t, srv, session, "GET", "/ui/api/devices/login/login", nil, headers, false)
	require.Equal(t, 200, res.StatusCode, out)
	result := out["results"].(map[string]any)
	require.Equal(t, "awaiting_code", result["state"])
	require.Equal(t, challenge, result["challenge"].(map[string]any)["challenge_id"])
	raw, _ := json.Marshal(result)
	require.NotContains(t, string(raw), "+15550000123")
	res, out = browserIntegrationRequest(t, srv, session, "POST", "/ui/api/devices/login/login", map[string]string{"phone": "+15550000123"}, headers, false)
	require.Equal(t, 429, res.StatusCode, out)
	require.Equal(t, "AUTH_RESEND_TOO_SOON", out["code"])
	require.NotEmpty(t, res.Header.Get("Retry-After"))
	res, out = browserIntegrationRequest(t, srv, session, "PATCH", "/ui/api/devices/login/webhook", map[string]any{"webhook_url": "https://example.test/hook", "webhook_secret": "never-expose-webhook-secret", "webhook_events": []string{"message"}}, headers, false)
	require.Equal(t, 200, res.StatusCode, out)
	res, out = browserIntegrationRequest(t, srv, session, "GET", "/ui/api/devices/login/webhook", nil, headers, false)
	require.Equal(t, 200, res.StatusCode, out)
	result = out["results"].(map[string]any)
	require.Equal(t, true, result["secret_configured"])
	require.Equal(t, "device", result["routing_mode"])
	raw, _ = json.Marshal(result)
	require.NotContains(t, string(raw), "never-expose-webhook-secret")
	// Preserve secret when only changing event filters.
	res, out = browserIntegrationRequest(t, srv, session, "PATCH", "/ui/api/devices/login/webhook", map[string]any{"webhook_events": []string{"message", "message.updated"}}, headers, false)
	require.Equal(t, 200, res.StatusCode, out)
	d, err = svc.GetDevice(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, "never-expose-webhook-secret", d.Webhook.Secret)
	_, err = st.AppendEvent(ctx, d.ConnectionID, domains.Event{ID: "test-message", Type: "message", Payload: json.RawMessage(`{"text":"<script>synthetic payload</script>"}`)}, []storage.WebhookTarget{{URL: d.Webhook.URL, Secret: d.Webhook.Secret, Device: true, Revision: d.Webhook.Revision}})
	require.NoError(t, err)
	res, out = browserIntegrationRequest(t, srv, session, "GET", "/ui/api/deliveries?state=queued&include_payload=false&limit=25", nil, headers, false)
	require.Equal(t, 200, res.StatusCode, out)
	deliveries := out["results"].([]any)
	require.Len(t, deliveries, 1)
	row := deliveries[0].(map[string]any)
	require.Nil(t, row["payload"])
	res, out = browserIntegrationRequest(t, srv, session, "GET", "/ui/api/deliveries/"+row["delivery_id"].(string), nil, headers, false)
	require.Equal(t, 200, res.StatusCode, out)
	require.NotNil(t, out["results"].(map[string]any)["payload"])
	res, out = browserIntegrationRequest(t, srv, session, "GET", "/ui/api/deliveries?state=invalid", nil, headers, false)
	require.Equal(t, 400, res.StatusCode, out)
	require.Equal(t, "INVALID_DELIVERY_STATE", out["code"])
	res, out = browserIntegrationRequest(t, srv, session, "GET", "/ui/api/deliveries?include_payload=invalid", nil, headers, false)
	require.Equal(t, 400, res.StatusCode, out)
}

func TestBrowserIntegrationBasePathAndDisabledUI(t *testing.T) {
	srv, _, _ := integrationBrowserServer(t, "/gateway", true, nil)
	session := integrationLogin(t, srv)
	require.Equal(t, "/gateway/ui/", session.cookie.Path)
	res, out := browserIntegrationRequest(t, srv, session, "GET", "/ui/api/devices", nil, nil, false)
	require.Equal(t, 200, res.StatusCode, out)
	raw, err := srv.App.Test(httptest.NewRequest("GET", "http://127.0.0.1:3017/gateway/ui/", nil))
	require.NoError(t, err)
	defer raw.Body.Close()
	body, err := io.ReadAll(raw.Body)
	require.NoError(t, err)
	require.Equal(t, 200, raw.StatusCode)
	require.Contains(t, string(body), `<base href="/gateway/ui/">`)
	require.NotContains(t, string(body), "__GOOMNI")
	off, _, _ := integrationBrowserServer(t, "/gateway", false, nil)
	for _, path := range []string{"/ui/", "/ui/auth/session", "/ui/api/devices"} {
		res, out := browserIntegrationRequest(t, off, browserIntegrationSession{}, "GET", path, nil, nil, true)
		require.Equal(t, 404, res.StatusCode, out)
		require.Equal(t, "NOT_FOUND", out["code"])
		require.True(t, strings.HasPrefix(res.Header.Get("Content-Type"), "application/json"))
	}
}

func TestBrowserIntegrationInFlightAliasReuseCannotPatchReplacement(t *testing.T) {
	srv, svc, _ := integrationBrowserServer(t, "", true, nil)
	session := integrationLogin(t, srv)
	ctx := context.Background()
	original, err := svc.CreateDevice(ctx, "shared", domains.ProviderBale)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listener := &bodyReadListener{Listener: ln, reading: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- srv.App.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { require.NoError(t, srv.App.ShutdownWithTimeout(5*time.Second)); require.NoError(t, <-done) })
	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	_, err = fmt.Fprintf(conn, "PATCH /ui/api/devices/shared/webhook HTTP/1.1\r\nHost: 127.0.0.1:3017\r\nOrigin: http://127.0.0.1:3017\r\nCookie: %s=%s\r\nX-CSRF-Token: %s\r\nX-Device-Instance: %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n", session.cookie.Name, session.cookie.Value, session.csrf, original.InstanceToken())
	require.NoError(t, err)
	select {
	case <-listener.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start reading body")
	}
	require.NoError(t, svc.DeleteDevice(ctx, original.ID))
	replacement, err := svc.CreateDevice(ctx, original.ID, domains.ProviderBale)
	require.NoError(t, err)
	body := `{"provider":"bale","webhook_url":"https://example.test/hook","webhook_secret":"synthetic"}`
	_, err = fmt.Fprintf(conn, "%x\r\n%s\r\n0\r\n\r\n", len(body), body)
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 404, response.StatusCode)
	current, err := svc.GetDevice(ctx, replacement.ID)
	require.NoError(t, err)
	require.Empty(t, current.Webhook.URL)
	require.Equal(t, replacement.ConnectionID, current.ConnectionID)
}

// Compare the actual cookie-authenticated route registry, not merely the source
// parser used by generation. A future route or loop cannot silently expand the
// browser surface or disappear from the API contract.
func TestBrowserIntegrationOpenAPIRegistryParity(t *testing.T) {
	srv, _, _ := integrationBrowserServer(t, "", true, nil)
	raw, err := os.ReadFile("../../../docs/openapi.yaml")
	require.NoError(t, err)
	type parameter struct {
		Name     string `yaml:"name"`
		In       string `yaml:"in"`
		Required bool   `yaml:"required"`
	}
	type operation struct {
		OperationID string                    `yaml:"operationId"`
		Security    []map[string][]string     `yaml:"security"`
		Parameters  []parameter               `yaml:"parameters"`
		RequestBody map[string]any            `yaml:"requestBody"`
		Responses   map[string]map[string]any `yaml:"responses"`
	}
	var spec struct {
		Paths map[string]map[string]operation `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &spec))
	actual, documented := map[string]bool{}, map[string]bool{}
	pathParameter := regexp.MustCompile(`:([a-z_]+)`)
	isBrowserPath := func(path string) bool { return path == "/ui/auth/session" || strings.HasPrefix(path, "/ui/api/") }
	for _, route := range srv.App.GetRoutes(true) {
		if route.Method == "HEAD" || !isBrowserPath(route.Path) {
			continue
		}
		actual[route.Method+" "+pathParameter.ReplaceAllString(route.Path, "{$1}")] = true
	}
	ids := map[string]bool{}
	for path, operations := range spec.Paths {
		for method, op := range operations {
			require.False(t, ids[op.OperationID], "duplicate operationId %s", op.OperationID)
			ids[op.OperationID] = true
			if !isBrowserPath(path) {
				continue
			}
			documented[strings.ToUpper(method)+" "+path] = true
			mutation := method != "get" && method != "head" && method != "options"
			params := map[string]parameter{}
			for _, p := range op.Parameters {
				params[p.In+":"+p.Name] = p
			}
			if path == "/ui/auth/session" && method == "post" {
				require.Empty(t, op.Security)
			} else {
				require.Equal(t, []map[string][]string{{"browserSession": {}}}, op.Security)
			}
			if mutation {
				require.True(t, params["header:Origin"].Required, path)
				if path != "/ui/auth/session" || method != "post" {
					require.True(t, params["header:X-CSRF-Token"].Required, path)
				}
			}
			if !strings.HasPrefix(path, "/ui/api/") {
				continue
			}
			basicPath := strings.TrimPrefix(path, "/ui/api")
			basic, exists := spec.Paths[basicPath][method]
			require.True(t, exists, path)
			require.Equal(t, basic.RequestBody, op.RequestBody, path+" request schema drifted")
			require.Equal(t, []map[string][]string{{"basicAuth": {}}}, basic.Security, "browser cloning changed Basic auth")
			for status, response := range basic.Responses {
				if status[0] == '2' {
					require.Equal(t, response, op.Responses[status], path+" success schema drifted")
				}
			}
			if strings.Contains(path, "{device_id}") || strings.HasPrefix(basicPath, "/deliveries") {
				require.True(t, params["header:X-Device-Instance"].Required, path)
				for _, p := range basic.Parameters {
					if p.Name == "X-Device-Instance" {
						require.True(t, p.Required, "machine API must require the immutable instance")
					}
				}
			}
			require.Contains(t, op.Responses["401"]["description"], "UI_UNAUTHORIZED")
			require.Contains(t, op.Responses["401"]["description"], "AUTH_REQUIRED")
			require.NotContains(t, path, "/send/")
			require.NotContains(t, path, "/operations/")
			require.NotContains(t, path, "/media")
		}
	}
	require.Equal(t, documented, actual)
}
