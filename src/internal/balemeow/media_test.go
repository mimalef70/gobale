package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func mediaClient(t *testing.T, f *fakeWS, server *httptest.Server, data []byte, info MediaInfo) *Client {
	t.Helper()
	u, e := url.Parse(server.URL)
	require.NoError(t, e)
	hc := server.Client()
	jar, e := cookiejar.New(nil)
	require.NoError(t, e)
	jar.SetCookies(u, []*http.Cookie{{Name: "account-cookie", Value: "private-cookie"}})
	hc.Jar = jar
	c := New(Options{WebSocketEndpoint: "ws" + strings.TrimPrefix(f.server.URL, "http"), HTTPClient: hc, MediaAllowedHosts: []string{u.Hostname()}, RequestTimeout: 200 * time.Millisecond, HandshakeTimeout: time.Second, PingInterval: time.Hour, MediaTimeout: time.Second, MaxMediaBytes: 1 << 20, MediaSource: func(context.Context, string) (io.ReadCloser, MediaInfo, error) {
		return io.NopCloser(bytes.NewReader(data)), info, nil
	}})
	connectTest(t, c, acceptingSink)
	return c
}
func respond(t *testing.T, f *fakeWS, ws *websocket.Conn, request *wire.Request, response proto.Message) {
	t.Helper()
	f.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: request.Index, Payload: marshal(t, response)}})
}
func mediaRequest(kind string) domains.SendRequest {
	return domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Kind: kind, MediaID: "account-scoped-id", RequestID: "123456789", Text: "caption", ReplyMessageID: "222"}
}

func TestMediaUploadUsesRawBytesThenDocumentSend(t *testing.T) {
	body := []byte("synthetic document contents")
	var puts, messages atomic.Int32
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		puts.Add(1)
		require.Equal(t, http.MethodPut, r.Method)
		require.Equal(t, "multipart/form-data", r.Header.Get("Content-Type"))
		require.Equal(t, int64(len(body)), r.ContentLength)
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		require.Empty(t, r.Header.Get("Token"))
		got, e := io.ReadAll(r.Body)
		require.NoError(t, e)
		require.Equal(t, body, got)
		w.WriteHeader(200)
	}))
	defer tls.Close()
	var f *fakeWS
	f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		switch m.Request.Method {
		case "GetNasimFileUploadUrl":
			var p wire.FileUploadRequest
			require.NoError(t, decode(m.Request.Payload, &p))
			require.Equal(t, int32(len(body)), p.ExpectedSize)
			require.Equal(t, int64(12345), p.Uid)
			require.Equal(t, "sample.txt", p.Name)
			require.Equal(t, int32(6), p.SendType.Type)
			respond(t, f, ws, m.Request, &wire.FileUploadResponse{FileId: 987654321, Url: tls.URL + "/upload?presigned=synthetic"})
		case "SendMessage":
			messages.Add(1)
			require.Equal(t, int32(1), puts.Load())
			var p wire.SendMessageRequest
			require.NoError(t, decode(m.Request.Payload, &p))
			require.Equal(t, int64(987654321), p.Message.Document.FileId)
			require.Equal(t, int64(12345), p.Message.Document.AccessHash)
			require.Equal(t, "caption", p.Message.Document.Caption.Text)
			require.Equal(t, int64(222), p.QuotedMessage.Rid)
			require.Equal(t, p.Peer, p.ExPeer)
			respond(t, f, ws, m.Request, &wire.SendMessageResponse{Sequence: 10, Date: 1720000000000})
		default:
			t.Errorf("unexpected media method %s", m.Request.Method)
		}
	})
	c := mediaClient(t, f, tls, body, MediaInfo{Name: "sample.txt", ContentType: "text/plain", Size: int64(len(body))})
	sent, e := c.Send(context.Background(), mediaRequest("file"))
	require.NoError(t, e)
	require.Equal(t, "123456789", sent.MessageID)
	require.Equal(t, int32(1), messages.Load())
}

func TestImageMetadataMatchesDecodedDimensions(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2, 3))))
	body := buf.Bytes()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, e := io.ReadAll(r.Body)
		require.NoError(t, e)
		require.Equal(t, body, got)
		w.WriteHeader(200)
	}))
	defer tls.Close()
	var f *fakeWS
	f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		switch m.Request.Method {
		case "GetNasimFileUploadUrl":
			respond(t, f, ws, m.Request, &wire.FileUploadResponse{FileId: 11, Url: tls.URL})
		case "SendMessage":
			var p wire.SendMessageRequest
			require.NoError(t, decode(m.Request.Payload, &p))
			require.Equal(t, int32(2), p.Message.Document.Ext.Photo.Width)
			require.Equal(t, int32(3), p.Message.Document.Ext.Photo.Height)
			respond(t, f, ws, m.Request, &wire.SendMessageResponse{Date: 1720000000000})
		}
	})
	c := mediaClient(t, f, tls, body, MediaInfo{Name: "image.png", ContentType: "image/png", Size: int64(len(body))})
	_, e := c.Send(context.Background(), mediaRequest("image"))
	require.NoError(t, e)
}

func TestInvalidMediaFailsBeforeAnyRPC(t *testing.T) {
	cases := []struct {
		name, kind string
		info       MediaInfo
		body       []byte
		code       string
	}{
		{"empty", "file", MediaInfo{Name: "a", ContentType: "text/plain", Size: 0}, nil, "MEDIA_TOO_LARGE"},
		{"oversize", "file", MediaInfo{Name: "a", ContentType: "text/plain", Size: 2 << 20}, nil, "MEDIA_TOO_LARGE"},
		{"bad mime", "file", MediaInfo{Name: "a", ContentType: "bad mime", Size: 3}, []byte("abc"), "INVALID_MEDIA"},
		{"wrong image bytes", "image", MediaInfo{Name: "a", ContentType: "image/png", Size: 3}, []byte("abc"), "INVALID_MEDIA"},
		{"wrong video mime", "video", MediaInfo{Name: "a", ContentType: "text/plain", Size: 3}, []byte("abc"), "INVALID_MEDIA"},
		{"negative duration", "audio", MediaInfo{Name: "a", ContentType: "audio/mpeg", Size: 3, Duration: -1}, []byte("abc"), "INVALID_MEDIA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			f := newFakeWS(t, func(*websocket.Conn, *wire.ClientMessage) { calls.Add(1) })
			tls := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid media reached HTTP") }))
			defer tls.Close()
			c := mediaClient(t, f, tls, tc.body, tc.info)
			_, e := c.Send(context.Background(), mediaRequest(tc.kind))
			require.Equal(t, tc.code, codeOf(e))
			require.Zero(t, calls.Load())
		})
	}
}

func TestMediaRedirectIsNotFollowedAndNoMessageIsSent(t *testing.T) {
	var redirected, messages atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer other.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer tls.Close()
	var f *fakeWS
	f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		if m.Request.Method == "SendMessage" {
			messages.Add(1)
			return
		}
		respond(t, f, ws, m.Request, &wire.FileUploadResponse{FileId: 11, Url: tls.URL})
	})
	c := mediaClient(t, f, tls, []byte("abc"), MediaInfo{Name: "a", ContentType: "text/plain", Size: 3})
	_, e := c.Send(context.Background(), mediaRequest("file"))
	require.Equal(t, "MEDIA_UPLOAD_REJECTED", codeOf(e))
	require.Zero(t, redirected.Load())
	require.Zero(t, messages.Load())
}

func TestMediaHostnameAllowlist(t *testing.T) {
	c := New(Options{})
	for _, raw := range []string{"http://cdn.bale.ai/a", "https://bale.ai.evil.test/a", "https://evil-bale.ai/a", "https://user:secret@bale.ai/a", "https://bale.ai/a#fragment", "https://127.0.0.1/a"} {
		_, e := c.validateMediaURL(raw)
		require.Equal(t, "UNTRUSTED_MEDIA_URL", codeOf(e), raw)
	}
	for _, raw := range []string{"https://bale.ai/a", "https://cdn.bale.ai/a"} {
		_, e := c.validateMediaURL(raw)
		require.NoError(t, e)
	}
}

func TestMediaSetupAndSendTimeoutClassification(t *testing.T) {
	for _, phase := range []string{"setup", "send"} {
		t.Run(phase, func(t *testing.T) {
			tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); w.WriteHeader(200) }))
			defer tls.Close()
			var messages atomic.Int32
			var f *fakeWS
			f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				if m.Request.Method == "SendMessage" {
					messages.Add(1)
					return
				}
				if phase == "setup" {
					return
				}
				respond(t, f, ws, m.Request, &wire.FileUploadResponse{FileId: 11, Url: tls.URL})
			})
			c := mediaClient(t, f, tls, []byte("abc"), MediaInfo{Name: "a", ContentType: "text/plain", Size: 3})
			_, e := c.Send(context.Background(), mediaRequest("file"))
			var de *domains.Error
			require.True(t, errors.As(e, &de))
			if phase == "setup" {
				require.Equal(t, "MEDIA_SETUP_FAILED", de.Code)
				require.False(t, de.Ambiguous)
				require.True(t, de.Retryable)
				require.Zero(t, messages.Load())
			} else {
				require.Equal(t, "SEND_UNKNOWN", de.Code)
				require.True(t, de.Ambiguous)
				require.Equal(t, int32(1), messages.Load())
			}
		})
	}
}

func TestDownloadValidatesIdentityAndBoundsStreamingBody(t *testing.T) {
	for _, mode := range []string{"wrong file", "encrypted", "declared oversize", "stream oversize", "normal"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				require.Empty(t, r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("Cookie"))
				if mode == "declared oversize" {
					w.Header().Set("Content-Length", "20")
					_, _ = w.Write([]byte(strings.Repeat("x", 20)))
					return
				}
				if mode == "stream oversize" {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					_, _ = w.Write([]byte(strings.Repeat("x", 20)))
					return
				}
				_, _ = w.Write([]byte("hello"))
			}))
			defer tls.Close()
			var f *fakeWS
			f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				require.Equal(t, "GetNasimFileUrl", m.Request.Method)
				location := &wire.FileURL{FileId: 11, Url: tls.URL}
				if mode == "wrong file" {
					location.FileId = 22
				}
				if mode == "encrypted" {
					location.Algorithm = "unsupported"
				}
				respond(t, f, ws, m.Request, &wire.FileDownloadResponse{FileUrl: location})
			})
			c := mediaClient(t, f, tls, nil, MediaInfo{})
			c.opts.MaxMediaBytes = 10
			body, e := c.Download(context.Background(), RemoteMedia{FileID: "11", AccessHash: "12345", Size: 5})
			switch mode {
			case "wrong file":
				require.Equal(t, "PROTOCOL_ERROR", codeOf(e))
				require.Zero(t, requests.Load())
			case "encrypted":
				require.Equal(t, "FEATURE_NOT_SUPPORTED", codeOf(e))
				require.Zero(t, requests.Load())
			case "declared oversize":
				require.Equal(t, "MEDIA_TOO_LARGE", codeOf(e))
			case "stream oversize":
				require.NoError(t, e)
				defer body.Close()
				b, e := io.ReadAll(body)
				require.Equal(t, "MEDIA_TOO_LARGE", codeOf(e))
				require.Len(t, b, 10)
			case "normal":
				require.NoError(t, e)
				defer body.Close()
				b, e := io.ReadAll(body)
				require.NoError(t, e)
				require.Equal(t, "hello", string(b))
			}
		})
	}
}

func TestNegativeSignedFileIDRoundTrip(t *testing.T) {
	body := []byte("signed-file-fixture")
	var sentFile atomic.Int64
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(200)
			return
		}
		_, _ = w.Write(body)
	}))
	defer tls.Close()
	const fileID int64 = -8123456789012345678
	var f *fakeWS
	f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		switch m.Request.Method {
		case "GetNasimFileUploadUrl":
			respond(t, f, ws, m.Request, &wire.FileUploadResponse{FileId: fileID, Url: tls.URL})
		case "SendMessage":
			var p wire.SendMessageRequest
			require.NoError(t, decode(m.Request.Payload, &p))
			sentFile.Store(p.Message.Document.FileId)
			respond(t, f, ws, m.Request, &wire.SendMessageResponse{Date: 1720000000000})
		case "GetNasimFileUrl":
			var p wire.FileDownloadRequest
			require.NoError(t, decode(m.Request.Payload, &p))
			require.Equal(t, fileID, p.File.FileId)
			respond(t, f, ws, m.Request, &wire.FileDownloadResponse{FileUrl: &wire.FileURL{FileId: fileID, Url: tls.URL}})
		}
	})
	c := mediaClient(t, f, tls, body, MediaInfo{Name: "signed.txt", ContentType: "text/plain", Size: int64(len(body))})
	_, e := c.Send(context.Background(), mediaRequest("file"))
	require.NoError(t, e)
	require.Equal(t, fileID, sentFile.Load())
	var downloader domains.MediaDownloader = c
	stream, e := downloader.Download(context.Background(), domains.ProviderMedia{FileID: "-8123456789012345678", AccessHash: "12345", Size: int64(len(body))})
	require.NoError(t, e)
	defer stream.Close()
	got, e := io.ReadAll(stream)
	require.NoError(t, e)
	require.Equal(t, body, got)
}

func TestUploadResponseFailureHasSafeDiagnosticCode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		code    string
	}{{"invalid protobuf", []byte{0xff}, "MEDIA_UPLOAD_RESPONSE_INVALID"}, {"missing reference", nil, "MEDIA_UPLOAD_REFERENCE_MISSING"}} {
		t.Run(tc.name, func(t *testing.T) {
			tls := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid reference triggered upload") }))
			defer tls.Close()
			var f *fakeWS
			f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request != nil {
					f.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: tc.payload}})
				}
			})
			c := mediaClient(t, f, tls, []byte("abc"), MediaInfo{Name: "a.txt", ContentType: "text/plain", Size: 3})
			_, e := c.Send(context.Background(), mediaRequest("file"))
			require.Equal(t, tc.code, codeOf(e))
		})
	}
}

func TestDeniedMediaDiagnosticContainsOnlySchemeAndHost(t *testing.T) {
	var diagnostics []Diagnostic
	c := New(Options{OnDiagnostic: func(d Diagnostic) { diagnostics = append(diagnostics, d) }})
	_, e := c.validateMediaURL("https://outside.example.test:9443/private-file-secret?signature=signature-secret#fragment-secret")
	require.Equal(t, "UNTRUSTED_MEDIA_URL", codeOf(e))
	require.Len(t, diagnostics, 1)
	require.Equal(t, "https", diagnostics[0].Scheme)
	require.Equal(t, "outside.example.test", diagnostics[0].Host)
	data, e := json.Marshal(diagnostics)
	require.NoError(t, e)
	for _, secret := range []string{"9443", "private-file-secret", "signature-secret", "fragment-secret"} {
		require.NotContains(t, string(data), secret)
	}
}

func TestObservedSilooUploadHostnameAllowlist(t *testing.T) {
	c := New(Options{})
	_, err := c.validateMediaURL("https://upload-ts-siloo.ble.ir/upload?signature=fixture")
	require.NoError(t, err)
	for _, denied := range []string{"http://upload-ts-siloo.ble.ir/upload", "https://other.ble.ir/upload", "https://upload-ts-siloo.ble.ir.attacker.test/upload", "https://upload-ts-siloo.ble.ir@attacker.test/upload"} {
		_, err = c.validateMediaURL(denied)
		require.Equal(t, "UNTRUSTED_MEDIA_URL", codeOf(err))
	}
}

func TestObservedProviderDownloadHostOnly(t *testing.T) {
	c := New(Options{})
	for _, allowed := range []string{"https://file-gw1.ble.ir/download?signature=fixture", "https://file-gw4.ble.ir/download", "https://file-gw12.ble.ir/download"} {
		if _, err := c.validateMediaURL(allowed); err != nil {
			t.Fatal(err)
		}
	}
	for _, denied := range []string{"http://file-gw4.ble.ir/download", "https://file-gw.ble.ir/download", "https://file-gwX.ble.ir/download", "https://x.file-gw4.ble.ir/download", "https://file-gw4.ble.ir.attacker.test/download", "https://file-gw4.ble.ir@attacker.test/download"} {
		if _, err := c.validateMediaURL(denied); err == nil {
			t.Fatalf("unobserved or insecure media host accepted: %s", denied)
		}
	}
}
