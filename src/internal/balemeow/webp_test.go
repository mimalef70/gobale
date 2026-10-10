package balemeow

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
)

func syntheticWebP(t *testing.T) []byte {
	t.Helper()
	// One uniform synthetic pixel, with no source metadata or identifying data.
	b, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)
	decoded, format, err := image.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	require.Equal(t, "webp", format)
	require.Equal(t, image.Rect(0, 0, 1, 1), decoded.Bounds())
	return b
}

func TestWebPImagePreservesBytesAndInspectedDimensions(t *testing.T) {
	body := syntheticWebP(t)
	var puts, sends atomic.Int32
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, body, data)
		puts.Add(1)
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
			var req wire.FileUploadRequest
			require.NoError(t, decode(m.Request.Payload, &req))
			require.Equal(t, "image/webp", req.MimeType)
			require.Equal(t, int32(len(body)), req.ExpectedSize)
			require.Equal(t, int32(1), req.SendType.Type)
			respond(t, f, ws, m.Request, &wire.FileUploadResponse{FileId: 11, Url: tls.URL})
		case "SendMessage":
			var req wire.SendMessageRequest
			require.NoError(t, decode(m.Request.Payload, &req))
			require.Equal(t, "image/webp", req.Message.Document.MimeType)
			require.Equal(t, int32(1), req.Message.Document.Ext.Photo.Width)
			require.Equal(t, int32(1), req.Message.Document.Ext.Photo.Height)
			sends.Add(1)
			respond(t, f, ws, m.Request, &wire.SendMessageResponse{Date: 1720000000000})
		}
	})
	c := mediaClient(t, f, tls, body, MediaInfo{Name: "image.webp", ContentType: "image/webp", Size: int64(len(body))})
	_, err := c.Send(context.Background(), mediaRequest("image"))
	require.NoError(t, err)
	require.EqualValues(t, 1, puts.Load())
	require.EqualValues(t, 1, sends.Load())
}

func TestWebPInvalidMIMEOrOversizedDimensionsNeverContactsProvider(t *testing.T) {
	body := syntheticWebP(t)
	// VP8X permits inspecting dimensions before allocating/decompressing pixels.
	oversized := append([]byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00"), []byte{0, 0, 0, 0, 0, 32, 0, 0, 0, 0}...)
	for _, tc := range []struct {
		name, mime string
		bytes      []byte
	}{
		{"mime mismatch", "image/png", body},
		{"invalid header", "image/webp", []byte("RIFF malformed webp")},
		{"dimension bound", "image/webp", oversized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			f := newFakeWS(t, func(*websocket.Conn, *wire.ClientMessage) { calls.Add(1) })
			tls := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid image reached HTTP") }))
			defer tls.Close()
			c := mediaClient(t, f, tls, tc.bytes, MediaInfo{Name: "synthetic.webp", ContentType: tc.mime, Size: int64(len(tc.bytes))})
			_, err := c.Send(context.Background(), mediaRequest("image"))
			require.Equal(t, "INVALID_MEDIA", codeOf(err))
			require.Zero(t, calls.Load())
		})
	}
}
