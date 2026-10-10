package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/mimalef70/goomni/src/ui/rest"
	"github.com/mimalef70/goomni/src/usecase"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

type loginFixtureClient struct {
	mu       sync.Mutex
	status   domains.ConnectionStatus
	inputs   map[string]string
	password bool
}

func (c *loginFixtureClient) StartAuth(_ context.Context, phone string) (domains.Challenge, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs["phone"] = phone
	c.status.Auth = "awaiting_code"
	return domains.Challenge{ID: "synthetic-challenge", State: "awaiting_code"}, nil
}
func (c *loginFixtureClient) SubmitCode(_ context.Context, challenge, code string) (*domains.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs["code"] = code
	c.inputs["challenge"] = challenge
	if c.password {
		c.status.Auth = "awaiting_password"
		return nil, nil
	}
	return &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "42", Token: "synthetic-token", DeviceHash: "synthetic-hash"}, nil
}
func (c *loginFixtureClient) SubmitPassword(_ context.Context, challenge, password string) (*domains.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs["password"] = password
	c.inputs["challenge"] = challenge
	return &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "42", Token: "synthetic-token", DeviceHash: "synthetic-hash"}, nil
}
func (c *loginFixtureClient) Connect(context.Context, *domains.Session, domains.Sink) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = domains.ConnectionStatus{Auth: "authenticated", Transport: "connected", Recovery: "degraded"}
	return nil
}
func (c *loginFixtureClient) Disconnect(context.Context) error { return nil }
func (c *loginFixtureClient) Logout(context.Context) error     { return nil }
func (c *loginFixtureClient) Status() domains.ConnectionStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}
func (c *loginFixtureClient) Send(context.Context, domains.SendRequest) (domains.SendResult, error) {
	return domains.SendResult{}, domains.Unsupported("fixture send")
}
func (c *loginFixtureClient) Call(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, domains.Unsupported("fixture call")
}

type loginFixtureRequest struct {
	method, path, key, instance, authorization string
	body                                       []byte
	status                                     int
}

type loginRESTFixture struct {
	store    *storage.Store
	service  *usecase.Service
	v        *viper.Viper
	mu       sync.Mutex
	requests []loginFixtureRequest
	clients  map[string]*loginFixtureClient
}

// Actual REST routing, validation, immutable guards and storage remain in the
// request path. The HTTP bridge only injects response loss or lifecycle races.
func newLoginRESTFixture(t *testing.T, password bool, after func(*loginRESTFixture, loginFixtureRequest) bool) *loginRESTFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := storage.Open(filepath.Join(dir, "fixture.db"), bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	f := &loginRESTFixture{store: st, clients: make(map[string]*loginFixtureClient)}
	f.service = usecase.New(st, usecase.Options{}, func(d domains.Device) domains.Client {
		client := &loginFixtureClient{password: password, inputs: make(map[string]string)}
		f.mu.Lock()
		f.clients[d.ConnectionID] = client
		f.mu.Unlock()
		return client
	})
	srv, err := rest.New(f.service, st, rest.Options{BasicAuth: "test:password", BasePath: "/api", MediaRoot: filepath.Join(dir, "media")})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read fixture request: %v", err)
			http.Error(w, "fixture failure", 500)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		response, err := srv.App.Test(r)
		if err != nil {
			t.Errorf("serve REST request: %v", err)
			http.Error(w, "fixture failure", 500)
			return
		}
		defer response.Body.Close()
		request := loginFixtureRequest{method: r.Method, path: r.URL.Path, key: r.Header.Get("Idempotency-Key"), instance: r.Header.Get("X-Device-Instance"), authorization: r.Header.Get("Authorization"), body: body, status: response.StatusCode}
		f.mu.Lock()
		f.requests = append(f.requests, request)
		f.mu.Unlock()
		if after != nil && after(f, request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("inject lost response: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	f.v = loginFixtureConfig(t, server.URL)
	f.v.Set("base-path", "/api")
	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, srv.App.ShutdownWithContext(ctx))
		require.NoError(t, f.service.Close(ctx))
		require.NoError(t, st.Close())
	})
	return f
}

func loginFixtureConfig(t *testing.T, serverURL string) *viper.Viper {
	t.Helper()
	host, portString, err := net.SplitHostPort(strings.TrimPrefix(serverURL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portString)
	require.NoError(t, err)
	v := viper.New()
	v.Set("host", host)
	v.Set("port", port)
	v.Set("basic-auth", "test:password")
	v.Set("master-key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'k'}, 32)))
	v.Set("max-media-bytes", 1024)
	return v
}

func (f *loginRESTFixture) snapshot() []loginFixtureRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]loginFixtureRequest(nil), f.requests...)
}
func (f *loginRESTFixture) providerInputs(connection string) map[string]string {
	f.mu.Lock()
	client := f.clients[connection]
	f.mu.Unlock()
	inputs := make(map[string]string)
	if client == nil {
		return inputs
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	for key, value := range client.inputs {
		inputs[key] = value
	}
	return inputs
}

func executeFixtureLogin(f *loginRESTFixture, reader func(string) (string, error)) error {
	cmd := loginCommandWithSecretReader(f.v, reader)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--provider", "bale", "--device", "test", "--phone", "+10000000000"})
	return cmd.Execute()
}

func TestLoginUsesProvisioningAndImmutableGuardsAgainstREST(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "existing"}[existing], func(t *testing.T) {
			f := newLoginRESTFixture(t, true, nil)
			ctx := context.Background()
			var before domains.Device
			if existing {
				var err error
				before, _, err = f.service.ProvisionDevice(ctx, domains.ProvisionDeviceRequest{Provider: domains.ProviderBale, DeviceID: "test", WebhookURL: "https://example.invalid/events", WebhookSecret: "synthetic-webhook-secret"}, "fixture-create")
				require.NoError(t, err)
			}
			password := "  synthetic password\u2003 "
			prompts := 0
			require.NoError(t, executeFixtureLogin(f, func(prompt string) (string, error) {
				prompts++
				if prompt == "Code: " {
					return "  synthetic-code  ", nil
				}
				require.Equal(t, "Two-step password: ", prompt)
				return password, nil
			}))
			require.Equal(t, 2, prompts)
			d, err := f.store.GetDevice(ctx, "test")
			require.NoError(t, err)
			require.Equal(t, "42", d.AccountID)
			if existing {
				require.Equal(t, before.ConnectionID, d.ConnectionID)
				require.Equal(t, before.Webhook, d.Webhook)
			}
			inputs := f.providerInputs(d.ConnectionID)
			require.Equal(t, "synthetic-code", inputs["code"])
			require.Equal(t, password, inputs["password"])
			require.Equal(t, "synthetic-challenge", inputs["challenge"])
			creates := 0
			for _, request := range f.snapshot() {
				require.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("test:password")), request.authorization)
				if request.method == "POST" && request.path == "/api/devices" {
					creates++
					require.NotEmpty(t, request.key)
					require.Equal(t, 201, request.status)
				}
				if strings.Contains(request.path, "/test/login") {
					require.Equal(t, d.InstanceID, request.instance)
					require.Equal(t, 200, request.status)
				}
			}
			if existing {
				require.Zero(t, creates, "existing selection must not re-provision or reset its webhook")
			} else {
				require.Equal(t, 1, creates)
			}
		})
	}
}

func TestLoginProvisioningLostResponseReusesKey(t *testing.T) {
	var dropped atomic.Bool
	f := newLoginRESTFixture(t, false, func(_ *loginRESTFixture, request loginFixtureRequest) bool {
		return request.method == "POST" && request.path == "/api/devices" && !dropped.Swap(true)
	})
	require.NoError(t, executeFixtureLogin(f, func(string) (string, error) { return "synthetic-code", nil }))
	var creates []loginFixtureRequest
	for _, request := range f.snapshot() {
		if request.method == "POST" && request.path == "/api/devices" {
			creates = append(creates, request)
		}
	}
	require.Len(t, creates, 2)
	require.NotEmpty(t, creates[0].key)
	require.Equal(t, creates[0].key, creates[1].key)
	require.Equal(t, creates[0].body, creates[1].body)
	require.Equal(t, 201, creates[0].status)
	require.Equal(t, 200, creates[1].status)
	devices, err := f.store.ListDevices(context.Background())
	require.NoError(t, err)
	require.Len(t, devices, 1)
	require.Equal(t, "42", devices[0].AccountID)
}

func TestLoginRejectsAliasReplacementDuringAuthentication(t *testing.T) {
	for _, stage := range []string{"login", "code", "password"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			replace := func(f *loginRESTFixture) error {
				if err := f.service.DeleteDevice(ctx, "test"); err != nil {
					return err
				}
				_, err := f.service.CreateDevice(ctx, "test", domains.ProviderBale)
				return err
			}
			f := newLoginRESTFixture(t, true, func(f *loginRESTFixture, request loginFixtureRequest) bool {
				if stage == "login" && request.method == "GET" && request.path == "/api/devices" {
					if err := replace(f); err != nil {
						t.Errorf("replace fixture device: %v", err)
					}
				}
				return false
			})
			original, err := f.service.CreateDevice(ctx, "test", domains.ProviderBale)
			require.NoError(t, err)
			err = executeFixtureLogin(f, func(prompt string) (string, error) {
				if (stage == "code" && prompt == "Code: ") || (stage == "password" && prompt == "Two-step password: ") {
					if err := replace(f); err != nil {
						return "", err
					}
				}
				return "synthetic-input", nil
			})
			require.ErrorContains(t, err, "DEVICE_INSTANCE_CHANGED")
			replacement, err := f.store.GetDevice(ctx, "test")
			require.NoError(t, err)
			require.NotEqual(t, original.InstanceID, replacement.InstanceID)
			require.Empty(t, replacement.AccountID)
			require.Empty(t, f.providerInputs(replacement.ConnectionID), "replacement must receive no authentication input")
			requests := f.snapshot()
			require.Equal(t, 409, requests[len(requests)-1].status)
			for _, request := range requests {
				if strings.Contains(request.path, "/test/login") {
					require.Equal(t, original.InstanceID, request.instance)
				}
			}
		})
	}
}

func TestLoginDoesNotAdoptConcurrentProvisioning(t *testing.T) {
	f := newLoginRESTFixture(t, false, func(f *loginRESTFixture, request loginFixtureRequest) bool {
		if request.method == "GET" && request.path == "/api/devices" {
			if _, err := f.service.CreateDevice(context.Background(), "test", domains.ProviderBale); err != nil {
				t.Errorf("create concurrent fixture device: %v", err)
			}
		}
		return false
	})
	err := executeFixtureLogin(f, func(string) (string, error) {
		t.Error("conflicting provisioning must not prompt for a code")
		return "", nil
	})
	require.ErrorContains(t, err, "DEVICE_EXISTS")
	requests := f.snapshot()
	require.Len(t, requests, 2)
	require.Equal(t, 409, requests[1].status)
}

func TestLoginLostProvisioningResponseCannotAdoptReusedAlias(t *testing.T) {
	var dropped atomic.Bool
	f := newLoginRESTFixture(t, false, func(f *loginRESTFixture, request loginFixtureRequest) bool {
		if request.method != "POST" || request.path != "/api/devices" || dropped.Swap(true) {
			return false
		}
		if err := f.service.DeleteDevice(context.Background(), "test"); err != nil {
			t.Errorf("delete original fixture device: %v", err)
		}
		if _, err := f.service.CreateDevice(context.Background(), "test", domains.ProviderBale); err != nil {
			t.Errorf("reuse fixture alias: %v", err)
		}
		return true
	})
	err := executeFixtureLogin(f, func(string) (string, error) {
		t.Error("retired provisioning must not prompt for authentication")
		return "", nil
	})
	require.ErrorContains(t, err, "PROVISIONING_RETIRED")
	requests := f.snapshot()
	require.Len(t, requests, 3)
	require.Equal(t, requests[1].key, requests[2].key)
	require.NotEmpty(t, requests[1].key)
	require.Equal(t, 409, requests[2].status)
	d, err := f.store.GetDevice(context.Background(), "test")
	require.NoError(t, err)
	require.Empty(t, f.providerInputs(d.ConnectionID))
}

func TestLoginDoesNotRetryAuthenticationAfterLostResponse(t *testing.T) {
	f := newLoginRESTFixture(t, false, func(_ *loginRESTFixture, request loginFixtureRequest) bool {
		return request.path == "/api/devices/test/login/code"
	})
	err := executeFixtureLogin(f, func(string) (string, error) { return "synthetic-code", nil })
	require.ErrorIs(t, err, errLoginAPIUnavailable)
	codeRequests := 0
	for _, request := range f.snapshot() {
		if request.path == "/api/devices/test/login/code" {
			codeRequests++
			require.Empty(t, request.key, "only provisioning is eligible for a safe retry")
		}
	}
	require.Equal(t, 1, codeRequests)
	d, err := f.store.GetDevice(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, "42", d.AccountID, "response loss must not imply authentication was uncommitted")
}
