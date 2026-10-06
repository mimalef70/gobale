package balemeow

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func marshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func codeOf(err error) string {
	var e *domains.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
func fakeSession() *domains.Session {
	return &domains.Session{UserID: "12345", Token: "synthetic-session-token", DeviceHash: "00112233445566778899aabbccddeeff00"}
}
func acceptingSink(context.Context, domains.Event) error { return nil }
func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

type fakeWS struct {
	t             *testing.T
	server        *httptest.Server
	dials         atomic.Int32
	connections   sync.Map
	handler       func(*websocket.Conn, *wire.ClientMessage)
	handshakeGate <-chan struct{}
}

func newFakeWS(t *testing.T, handler func(*websocket.Conn, *wire.ClientMessage)) *fakeWS {
	t.Helper()
	f := &fakeWS{t: t, handler: handler}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.dials.Add(1)
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		f.connections.Store(ws, true)
		defer f.connections.Delete(ws)
		defer ws.CloseNow()
		for {
			typ, b, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			if typ != websocket.MessageBinary {
				return
			}
			msg := &wire.ClientMessage{}
			if decode(b, msg) != nil {
				return
			}
			if msg.Handshake != nil {
				if f.handshakeGate != nil {
					<-f.handshakeGate
				}
				f.send(ws, &wire.ServerMessage{Handshake: &wire.HandshakeResponse{ProtocolVersion: 1, ApiVersion: 1}})
			} else if f.handler != nil {
				f.handler(ws, msg)
			} else if msg.Ping != nil {
				f.send(ws, &wire.ServerMessage{Pong: msg.Ping})
			}
		}
	}))
	t.Cleanup(func() {
		f.connections.Range(func(k, v any) bool { _ = k.(*websocket.Conn).CloseNow(); return true })
		f.server.Close()
	})
	return f
}
func (f *fakeWS) send(ws *websocket.Conn, msg *wire.ServerMessage) {
	b, err := proto.Marshal(msg)
	if err != nil {
		f.t.Error(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = ws.Write(ctx, websocket.MessageBinary, b)
}
func (f *fakeWS) client() *Client {
	return New(Options{WebSocketEndpoint: "ws" + strings.TrimPrefix(f.server.URL, "http"), RequestTimeout: 100 * time.Millisecond, HandshakeTimeout: time.Second, PingInterval: time.Hour})
}
func connectTest(t *testing.T, c *Client, sink domains.Sink) {
	t.Helper()
	if err := c.Connect(context.Background(), fakeSession(), sink); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Disconnect(context.Background()) })
}

func TestAuthNativeGRPCWeb(t *testing.T) {
	var requests atomic.Int32
	var transactionHash string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Content-Type") != "application/grpc-web+proto" {
			t.Error("wrong content type")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if len(body) < 5 || body[0] != 0 || int(binary.BigEndian.Uint32(body[1:5])) != len(body)-5 {
			t.Error("bad request frame")
			return
		}
		var result proto.Message
		switch {
		case strings.HasSuffix(r.URL.Path, "StartPhoneAuth"):
			req := &wire.StartPhoneAuthRequest{}
			if decode(body[5:], req) != nil {
				t.Error("decode")
			}
			if req.PhoneNumber != 989123456789 || req.AppId != 99 || req.ApiKey != "synthetic-app-key" || len(req.DeviceHash) != 16 {
				t.Error("auth identity mismatch")
			}
			result = &wire.StartPhoneAuthResponse{TransactionHash: "synthetic-transaction"}
		case strings.HasSuffix(r.URL.Path, "ValidateCode"):
			req := &wire.ValidateCodeRequest{}
			if decode(body[5:], req) != nil {
				t.Error("decode")
			}
			transactionHash = req.TransactionHash
			if req.Code != "12345" || !req.IsJwt.Value {
				t.Error("code mismatch")
			}
			result = &wire.AuthResponse{User: &wire.User{Id: 12345}, Jwt: &wire.StringValue{Value: "synthetic-token"}}
		default:
			t.Error("unexpected method")
			return
		}
		w.Header().Set("Content-Type", "application/grpc-web+proto")
		_, _ = w.Write(grpcFrame(0, marshal(t, result)))
		_, _ = w.Write(grpcFrame(128, []byte("grpc-status: 0\r\n")))
	}))
	defer server.Close()
	c := New(Options{GRPCEndpoint: server.URL, AppID: 99, APIKey: "synthetic-app-key"})
	challenge, err := c.StartAuth(context.Background(), "۰۹۱۲۳۴۵۶۷۸۹")
	if err != nil {
		t.Fatal(err)
	}
	if challenge.State != "awaiting_code" || challenge.ID == "synthetic-transaction" {
		t.Fatal("provider auth hash must not be public challenge ID")
	}
	session, err := c.SubmitCode(context.Background(), challenge.ID, "۱۲۳۴۵")
	if err != nil {
		t.Fatal(err)
	}
	if session.UserID != "12345" || session.Token != "synthetic-token" || transactionHash != "synthetic-transaction" || requests.Load() != 2 {
		t.Fatal("unexpected authentication result")
	}
	if _, err = c.SubmitCode(context.Background(), challenge.ID, "12345"); codeOf(err) != "CHALLENGE_EXPIRED" {
		t.Fatalf("old challenge accepted: %v", err)
	}
}

func TestAuthRequiresConfiguredIdentity(t *testing.T) {
	c := New(Options{})
	_, err := c.StartAuth(context.Background(), "+989123456789")
	if codeOf(err) != "CLIENT_IDENTITY_REQUIRED" {
		t.Fatal(err)
	}
}
func TestAuthRedirectIsNotFollowed(t *testing.T) {
	var forwarded atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	c := New(Options{GRPCEndpoint: src.URL, AppID: 1, APIKey: "synthetic"})
	_, err := c.StartAuth(context.Background(), "+989123456789")
	if codeOf(err) != "AUTH_HTTP_ERROR" || forwarded.Load() != 0 {
		t.Fatalf("redirect followed: %v %d", err, forwarded.Load())
	}
}
func TestPasswordRequiredHasExplicitState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "StartPhoneAuth"):
			_, _ = w.Write(grpcFrame(0, marshal(t, &wire.StartPhoneAuthResponse{TransactionHash: "synthetic"})))
			_, _ = w.Write(grpcFrame(128, []byte("grpc-status: 0\r\n")))
		case strings.HasSuffix(r.URL.Path, "ValidateCode"):
			_, _ = w.Write(grpcFrame(128, []byte("grpc-status: 9\r\ngrpc-message: PASSWORD_REQUIRED\r\n")))
		default:
			_, _ = w.Write(grpcFrame(0, marshal(t, &wire.AuthResponse{User: &wire.User{Id: 7}, Jwt: &wire.StringValue{Value: "synthetic"}})))
			_, _ = w.Write(grpcFrame(128, []byte("grpc-status: 0\r\n")))
		}
	}))
	defer server.Close()
	c := New(Options{GRPCEndpoint: server.URL, AppID: 1, APIKey: "synthetic"})
	ch, err := c.StartAuth(context.Background(), "+989123456789")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.SubmitCode(context.Background(), ch.ID, "12345")
	if err != nil || s != nil || c.Status().Auth != "awaiting_password" {
		t.Fatalf("wrong state: %v %v", s, err)
	}
	s, err = c.SubmitPassword(context.Background(), ch.ID, "synthetic-password")
	if err != nil || s == nil || s.UserID != "7" {
		t.Fatalf("password auth: %v", err)
	}
}
func TestMissingAuthJWTDoesNotImplyTwoFactor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m proto.Message = &wire.AuthResponse{User: &wire.User{Id: 7}}
		if strings.HasSuffix(r.URL.Path, "StartPhoneAuth") {
			m = &wire.StartPhoneAuthResponse{TransactionHash: "synthetic"}
		}
		_, _ = w.Write(grpcFrame(0, marshal(t, m)))
		_, _ = w.Write(grpcFrame(128, []byte("grpc-status: 0\r\n")))
	}))
	defer server.Close()
	c := New(Options{GRPCEndpoint: server.URL, AppID: 1, APIKey: "synthetic"})
	ch, err := c.StartAuth(context.Background(), "+989123456789")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SubmitCode(context.Background(), ch.ID, "12345")
	if codeOf(err) != "PROTOCOL_ERROR" || c.Status().Auth != "awaiting_code" {
		t.Fatalf("guessed 2FA: %v", err)
	}
}

func TestConnectSingleflightAndNoFalseRecovery(t *testing.T) {
	gate := make(chan struct{})
	fake := newFakeWS(t, nil)
	fake.handshakeGate = gate
	c := fake.client()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- c.Connect(context.Background(), fakeSession(), acceptingSink) }()
	}
	eventually(t, func() bool { return fake.dials.Load() == 1 })
	time.Sleep(10 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	defer c.Disconnect(context.Background())
	if fake.dials.Load() != 1 || c.Status().Transport != "connected" || c.Status().Recovery != "degraded" {
		t.Fatal("singleflight/recovery invariant failed")
	}
}
func TestNativeTextUsesPersistedInt64RID(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		if m.Request.Service != "bale.messaging.v2.Messaging" || m.Request.Method != "SendMessage" {
			t.Error("wrong RPC")
		}
		req := &wire.SendMessageRequest{}
		if decode(m.Request.Payload, req) != nil {
			t.Error("bad send")
		}
		if req.Rid != 9007199254740993 || req.Peer.Id != 77 || req.Message.Text.Text != "hello" {
			t.Error("request lost ID precision")
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.SendMessageResponse{Date: 1720000000000, Sequence: 32516})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	result, err := c.Send(context.Background(), domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "77"}, Text: "hello", RequestID: "9007199254740993"})
	if err != nil || result.MessageID != "9007199254740993" || result.Date.UnixMilli() != 1720000000000 {
		t.Fatalf("result: %+v %v", result, err)
	}
}
func TestUnknownSendNeverRetried(t *testing.T) {
	var calls atomic.Int32
	fake := newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			calls.Add(1)
			_ = ws.CloseNow()
		}
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	_, err := c.Send(context.Background(), domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "77"}, Text: "hello", RequestID: "99"})
	var e *domains.Error
	if !errors.As(err, &e) || !e.Ambiguous {
		t.Fatalf("send was not ambiguous: %v", err)
	}
	if calls.Load() != 1 || fake.dials.Load() != 1 {
		t.Fatal("ambiguous send retried")
	}
}
func TestPendingRPCBound(t *testing.T) {
	entered := make(chan struct{}, 1)
	fake := newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			entered <- struct{}{}
		}
	})
	c := fake.client()
	c.opts.MaxPending = 1
	connectTest(t, c, acceptingSink)
	first := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), "message.read", json.RawMessage(`{"peer":{"type":"user","id":"77"},"date":"123"}`))
		first <- err
	}()
	<-entered
	_, err := c.Call(context.Background(), "message.read", json.RawMessage(`{"peer":{"type":"user","id":"77"},"date":"123"}`))
	if codeOf(err) != "PROVIDER_BACKPRESSURE" {
		t.Fatalf("no backpressure: %v", err)
	}
	if codeOf(<-first) != "SEND_UNKNOWN" {
		t.Fatal("timed out request should be unknown")
	}
}
func TestUnsupportedSendIsRejectedBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	fake := newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) { calls.Add(1) })
	c := fake.client()
	connectTest(t, c, acceptingSink)
	cases := []domains.SendRequest{
		{Peer: domains.Peer{Type: "user", ID: "1"}, Kind: "image", MediaID: "local", RequestID: "1"},
	}
	for _, r := range cases {
		_, err := c.Send(context.Background(), r)
		if codeOf(err) != "FEATURE_NOT_SUPPORTED" {
			t.Fatalf("expected unsupported: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported action sent provider request")
	}
}
func TestSessionRevokedStopsConnection(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			fake.send(ws, &wire.ServerMessage{TerminateSession: &wire.Empty{}})
		}
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	_, _ = c.Call(context.Background(), "message.read", json.RawMessage(`{"peer":{"type":"user","id":"77"},"date":"123"}`))
	eventually(t, func() bool { return c.Status().Auth == "auth_required" })
	if err := c.Connect(context.Background(), fakeSession(), acceptingSink); codeOf(err) != "AUTH_REQUIRED" {
		t.Fatalf("revoked session reused: %v", err)
	}
}
func TestHeartbeatRequiresMatchingPong(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Ping != nil {
			fake.send(ws, &wire.ServerMessage{Pong: &wire.Ping{Id: m.Ping.Id + 1}})
		}
	})
	c := fake.client()
	c.opts.PingInterval = 10 * time.Millisecond
	c.opts.PingTimeout = 10 * time.Millisecond
	connectTest(t, c, acceptingSink)
	eventually(t, func() bool { return c.Status().LastError == "HEARTBEAT_TIMEOUT" })
}
func TestEventPersistenceFailureDisconnects(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			update := &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 1, Id: 77}, SenderId: 77, Date: 1720000000000, Rid: 9007199254740993, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}}}
			fake.send(ws, &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Update: marshal(t, update)})}})
		}
	})
	c := fake.client()
	connectTest(t, c, func(ctx context.Context, event domains.Event) error { return errors.New("synthetic disk full") })
	_, _ = c.Call(context.Background(), "message.read", json.RawMessage(`{"peer":{"type":"user","id":"77"},"date":"123"}`))
	eventually(t, func() bool { return c.Status().LastError == "EVENT_PERSIST_FAILED" })
	if c.Status().Recovery == "current" {
		t.Fatal("false recovery claim")
	}
}
func TestReadAndEditPayloadValidation(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body string }{
		{"message.read", `{"peer":{"type":"user","id":"1"},"date":"0"}`},
		{"message.edit", `{"peer":{"type":"user","id":"1"},"message_id":"1","message":""}`},
	} {
		_, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(err) != "INVALID_REQUEST" {
			t.Fatal(err)
		}
	}
	if _, err := c.Call(context.Background(), "history", json.RawMessage(`{}`)); codeOf(err) != "FEATURE_NOT_SUPPORTED" {
		t.Fatal(err)
	}
}
func TestLogoutFailureDoesNotPretendRevoke(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	c := New(Options{GRPCEndpoint: server.URL})
	c.session = fakeSession()
	c.status.Auth = "authenticated"
	if err := c.Logout(context.Background()); err == nil {
		t.Fatal("logout without provider confirmation succeeded")
	}
	if c.session == nil || c.Status().Auth != "authenticated" {
		t.Fatal("failed logout erased evidence")
	}
}
