package balemeow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestClientCookieJarsAreIsolated(t *testing.T) {
	sharedJar, e := cookiejar.New(nil)
	require.NoError(t, e)
	endpoint, e := url.Parse("https://next-ws.bale.ai/ws/")
	require.NoError(t, e)
	sharedJar.SetCookies(endpoint, []*http.Cookie{{Name: "unrelated", Value: "do-not-inherit", Path: "/"}})
	shared := &http.Client{Jar: sharedJar}
	a := New(Options{HTTPClient: shared})
	b := New(Options{HTTPClient: shared})
	require.Empty(t, a.opts.HTTPClient.Jar.Cookies(endpoint))
	require.Empty(t, b.opts.HTTPClient.Jar.Cookies(endpoint))
	a.opts.HTTPClient.Jar.SetCookies(endpoint, []*http.Cookie{{Name: "session", Value: "account-a-cookie", Path: "/"}})
	require.Len(t, a.opts.HTTPClient.Jar.Cookies(endpoint), 1)
	require.Empty(t, b.opts.HTTPClient.Jar.Cookies(endpoint))
	require.Equal(t, "unrelated", sharedJar.Cookies(endpoint)[0].Name)
}

func TestNativeAuthPersistsOwnCookiesAndRestoresJWTHandshake(t *testing.T) {
	var wsRequests, rpcCalls atomic.Int32
	const token = "synthetic-auth-jwt"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/" {
			wsRequests.Add(1)
			require.Equal(t, token, r.Header.Get("auth-jwt"))
			require.Empty(t, r.URL.Query().Get("uid"))
			cookie, e := r.Cookie("native-session")
			require.NoError(t, e)
			require.Equal(t, "synthetic-native-cookie", cookie.Value)
			ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			require.NoError(t, e)
			defer ws.CloseNow()
			for {
				_, data, e := ws.Read(r.Context())
				if e != nil {
					return
				}
				var m wire.ClientMessage
				require.NoError(t, decode(data, &m))
				var out *wire.ServerMessage
				if m.Handshake != nil {
					out = &wire.ServerMessage{Handshake: &wire.HandshakeResponse{ProtocolVersion: 1, ApiVersion: m.Handshake.ApiVersion}}
				} else if m.Request != nil {
					rpcCalls.Add(1)
					metadata := map[string]string{}
					for _, kv := range m.Request.Metadata.Items {
						metadata[kv.Key] = kv.Value.StringValue
					}
					require.Equal(t, token, metadata["auth-jwt"])
					require.Empty(t, metadata["token"])
					require.Empty(t, metadata["mt_token"])
					out = &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index}}
				} else if m.Ping != nil {
					out = &wire.ServerMessage{Pong: m.Ping}
				}
				if out != nil {
					data, e = proto.Marshal(out)
					require.NoError(t, e)
					ctx, cancel := context.WithTimeout(r.Context(), time.Second)
					e = ws.Write(ctx, websocket.MessageBinary, data)
					cancel()
					if e != nil {
						return
					}
				}
			}
		}
		var response proto.Message
		switch {
		case strings.HasSuffix(r.URL.Path, "StartPhoneAuth"):
			response = &wire.StartPhoneAuthResponse{TransactionHash: "synthetic-transaction"}
		case strings.HasSuffix(r.URL.Path, "ValidateCode"):
			http.SetCookie(w, &http.Cookie{Name: "native-session", Value: "synthetic-native-cookie", Path: "/", Secure: true, HttpOnly: true})
			response = &wire.AuthResponse{User: &wire.User{Id: 12345}, Jwt: &wire.StringValue{Value: token}}
		default:
			t.Errorf("unexpected native auth endpoint %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		payload, e := proto.Marshal(response)
		require.NoError(t, e)
		w.Header().Set("Content-Type", "application/grpc-web+proto")
		_, _ = w.Write(append(grpcFrame(0, payload), grpcFrame(128, []byte("grpc-status: 0\r\n"))...))
	}))
	defer server.Close()
	opts := Options{GRPCEndpoint: server.URL, WebSocketEndpoint: "wss" + strings.TrimPrefix(server.URL, "https") + "/ws/", HTTPClient: server.Client(), AppID: 1, APIKey: "synthetic-app-key", PingInterval: time.Hour, RequestTimeout: time.Second}
	original := New(opts)
	ch, e := original.StartAuth(context.Background(), "+989123456789")
	require.NoError(t, e)
	session, e := original.SubmitCode(context.Background(), ch.ID, "12345")
	require.NoError(t, e)
	require.NotNil(t, session)
	require.Contains(t, string(session.Data), "synthetic-native-cookie")
	unrelated := New(opts)
	for _, origin := range unrelated.cookieOrigins() {
		require.Empty(t, unrelated.opts.HTTPClient.Jar.Cookies(origin))
	}
	restored := New(opts)
	require.NoError(t, restored.Connect(context.Background(), session, acceptingSink))
	defer restored.Disconnect(context.Background())
	_, e = restored.Call(context.Background(), "message.read", json.RawMessage(`{"peer":{"type":"user","id":"42"},"date":"1720000000000"}`))
	require.NoError(t, e)
	require.Equal(t, int32(1), wsRequests.Load())
	require.Equal(t, int32(1), rpcCalls.Load())
}

func TestCookieSnapshotRejectsChangedEndpointBeforeNetwork(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	c := New(Options{GRPCEndpoint: server.URL, WebSocketEndpoint: "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/"})
	session := fakeSession()
	session.Data = json.RawMessage(`{"cookies":[{"origin":"https://other.example.test","cookies":[{"Name":"session","Value":"private"}]}]}`)
	require.Equal(t, "INVALID_SESSION", codeOf(c.Connect(context.Background(), session, acceptingSink)))
	require.Zero(t, hits.Load())
	for _, origin := range c.cookieOrigins() {
		require.Empty(t, c.opts.HTTPClient.Jar.Cookies(origin))
	}
}

func TestInvalidCookieSnapshotCannotPartiallyMutateJar(t *testing.T) {
	c := New(Options{GRPCEndpoint: "https://api.bale.ai", WebSocketEndpoint: "wss://next-ws.bale.ai/ws/"})
	raw, e := json.Marshal(sessionData{Cookies: []cookieSnapshot{{Origin: "https://api.bale.ai", Cookies: []*http.Cookie{{Name: "partial", Value: "must-not-install"}}}, {Origin: "https://evil.example", Cookies: []*http.Cookie{{Name: "other", Value: "private"}}}}})
	require.NoError(t, e)
	require.Equal(t, "INVALID_SESSION", codeOf(c.restoreCookies(raw)))
	for _, origin := range c.cookieOrigins() {
		require.Empty(t, c.opts.HTTPClient.Jar.Cookies(origin), "rejected snapshot changed private jar")
	}
}

func TestClearCookiesRemovesDomainAndHostCookies(t *testing.T) {
	c := New(Options{GRPCEndpoint: "https://api.bale.ai", WebSocketEndpoint: "wss://next-ws.bale.ai/ws/"})
	origin, _ := url.Parse("https://api.bale.ai")
	c.opts.HTTPClient.Jar.SetCookies(origin, []*http.Cookie{{Name: "domain", Value: "private-domain", Domain: ".bale.ai", Path: "/", Secure: true}, {Name: "host", Value: "private-host", Path: "/"}})
	require.Len(t, c.opts.HTTPClient.Jar.Cookies(origin), 2)
	c.clearCookies()
	for _, endpoint := range c.cookieOrigins() {
		require.Empty(t, c.opts.HTTPClient.Jar.Cookies(endpoint), "logout left a provider session cookie")
	}
}
