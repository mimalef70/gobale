package rest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

type scopedHTTPClient struct {
	testClient
	account string
	calls   *atomic.Int64
}

func (f *scopedHTTPClient) SubmitCode(context.Context, string, string) (*domains.Session, error) {
	f.calls.Add(1)
	return &domains.Session{UserID: f.account, Token: "synthetic-token", DeviceHash: "synthetic-hash"}, nil
}
func (f *scopedHTTPClient) SubmitPassword(ctx context.Context, challenge, password string) (*domains.Session, error) {
	return f.SubmitCode(ctx, challenge, password)
}
func (f *scopedHTTPClient) StartAuth(ctx context.Context, phone string) (domains.Challenge, error) {
	f.calls.Add(1)
	return f.testClient.StartAuth(ctx, phone)
}
func (f *scopedHTTPClient) Send(_ context.Context, request domains.SendRequest) (domains.SendResult, error) {
	f.calls.Add(1)
	return domains.SendResult{MessageID: request.RequestID, Date: time.Now()}, nil
}
func (f *scopedHTTPClient) Call(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	f.calls.Add(1)
	return json.RawMessage(`{}`), nil
}

type bodyReadListener struct {
	net.Listener
	reading chan struct{}
}

func (l *bodyReadListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &bodyReadConn{Conn: c, reading: l.reading}, nil
}

type bodyReadConn struct {
	net.Conn
	header  string
	reading chan struct{}
	once    sync.Once
}

func (c *bodyReadConn) Read(p []byte) (int, error) {
	if strings.Contains(c.header, "\r\n\r\n") {
		c.once.Do(func() { close(c.reading) })
	}
	n, err := c.Conn.Read(p)
	if !strings.Contains(c.header, "\r\n\r\n") {
		c.header += string(p[:n])
	}
	return n, err
}

func TestHTTPAccountSelectionSurvivesAliasReuse(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	scheduled := fmt.Sprintf(`{"kind":"text","peer":{"type":"user","id":"42"},"message":"scheduled","scheduled_at":%q,"timezone":"UTC"}`, future)
	for _, tc := range []struct {
		name, method, path, body string
		header                   bool
	}{
		{"send-header", "POST", "/send/message", `{"peer":{"type":"user","id":"42"},"message":"original account"}`, true},
		{"send-query", "POST", "/send/message?device_id=shared", `{"peer":{"type":"user","id":"42"},"message":"original account"}`, false},
		{"schedule", "POST", "/send/schedules", scheduled, true},
		{"scheduled-send", "POST", "/send/message", scheduled, true},
		{"provider-read", "GET", "/user/info", `{}`, true},
		{"provider-mutation", "POST", "/group", `{"title":"Synthetic group","users":[{"type":"user","id":"42"}]}`, true},
		{"generic-operation", "POST", "/operations/account.name", `{"name":"Synthetic name"}`, true},
		{"login", "POST", "/devices/shared/login", `{"phone":"+10000000000"}`, false},
		{"login-code", "POST", "/devices/shared/login/code", `{"challenge_id":"synthetic","code":"synthetic"}`, false},
		{"login-password", "POST", "/devices/shared/login/password", `{"challenge_id":"synthetic","password":"synthetic"}`, false},
		{"webhook", "PATCH", "/devices/shared/webhook", `{"webhook_url":"https://example.invalid/events","webhook_secret":"synthetic"}`, false},
		{"media-upload", "POST", "/media", `synthetic media`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var identities, calls atomic.Int64
			srv, svc := setupAPIWithFactory(t, "", func(domains.Device) domains.Client {
				return &scopedHTTPClient{account: fmt.Sprint(1000 + identities.Add(1)), calls: &calls}
			})
			ctx := context.Background()
			original, err := svc.CreateDevice(ctx, "shared")
			require.NoError(t, err)
			challenge, err := svc.StartAuth(ctx, original.ID, "+10000000000")
			require.NoError(t, err)
			_, err = svc.SubmitCode(ctx, original.ID, challenge.ID, "synthetic")
			require.NoError(t, err)
			original, err = svc.GetDevice(ctx, original.ID)
			require.NoError(t, err)
			require.Equal(t, "1001", original.AccountID)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			listener := &bodyReadListener{Listener: ln, reading: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- srv.App.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()
			t.Cleanup(func() {
				require.NoError(t, srv.App.ShutdownWithTimeout(5*time.Second))
				require.NoError(t, <-done)
			})
			conn, err := net.Dial("tcp", ln.Addr().String())
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
			header := "X-Device-Instance: " + original.InstanceToken() + "\r\n"
			if tc.header {
				header += "X-Device-Id: shared\r\n"
			}
			_, err = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Basic dGVzdDpwYXNzd29yZA==\r\n%sContent-Type: application/json\r\nIdempotency-Key: scope-regression\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n", tc.method, tc.path, header)
			require.NoError(t, err)
			// A chunked body is streamed: the second socket read occurs after the
			// handler has selected the account, while its body is still pending.
			select {
			case <-listener.reading:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not start reading the request body")
			}
			require.NoError(t, svc.DeleteDevice(ctx, original.ID))
			replacement, err := svc.CreateDevice(ctx, original.ID)
			require.NoError(t, err)
			require.NotEqual(t, original.ConnectionID, replacement.ConnectionID)
			challenge, err = svc.StartAuth(ctx, replacement.ID, "+10000000000")
			require.NoError(t, err)
			_, err = svc.SubmitCode(ctx, replacement.ID, challenge.ID, "synthetic")
			require.NoError(t, err)
			replacement, err = svc.GetDevice(ctx, replacement.ID)
			require.NoError(t, err)
			require.Equal(t, "1002", replacement.AccountID)
			before := calls.Load()
			_, err = fmt.Fprintf(conn, "%x\r\n%s\r\n0\r\n\r\n", len(tc.body), tc.body)
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, response.StatusCode, string(body))
			require.Equal(t, before, calls.Load(), "stale request contacted the replacement account")
			ops, err := srv.store.ListOperations(ctx, replacement.ConnectionID, 100, 0)
			require.NoError(t, err)
			require.Empty(t, ops)
			jobs, err := srv.store.ListSchedules(ctx, replacement.ConnectionID, 100, 0)
			require.NoError(t, err)
			require.Empty(t, jobs)
			current, err := svc.GetDevice(ctx, replacement.ID)
			require.NoError(t, err)
			require.Empty(t, current.Webhook.URL)
		})
	}
}
