package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

func TestGroupPhotoUploadsScopedImageBeforeAvatarMutation(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	var uploads, mutations atomic.Int32
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, body) || r.Method != http.MethodPut || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("invalid upload or credential leakage")
		}
		uploads.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer tls.Close()
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		switch m.Request.Method {
		case "GetNasimFileUploadUrl":
			q := &wire.FileUploadRequest{}
			if decode(m.Request.Payload, q) != nil || q.Uid != 12345 || q.MimeType != "image/png" || q.ExPeer != nil || q.SendType != nil {
				t.Error("invalid profile upload request")
			}
			respond(t, fake, ws, m.Request, &wire.FileUploadResponse{FileId: -987, Url: tls.URL + "/upload"})
		case "EditGroupAvatar":
			q := &wire.GroupPhotoRequest{}
			if decode(m.Request.Payload, q) != nil || q.Rid != 99 || q.Group.GetAccessHash() != 456 || q.FileLocation.GetFileId() != -987 || q.FileLocation.GetAccessHash() != 12345 || uploads.Load() != 1 {
				t.Error("avatar mutation happened before valid upload")
			}
			mutations.Add(1)
			respond(t, fake, ws, m.Request, &wire.Empty{})
		default:
			t.Errorf("unexpected RPC %s", m.Request.Method)
		}
	})
	c := mediaClient(t, fake, tls, body, MediaInfo{Name: "avatar.png", ContentType: "image/png", Size: int64(len(body))})
	c.rememberRef("group", &wire.PeerRef{Id: 77, AccessHash: 456})
	data, err := c.groupExtended(context.Background(), "group.photo", json.RawMessage(`{"peer":{"type":"group","id":"77"},"media_id":"scoped-media","request_id":"99"}`))
	if err != nil || mutations.Load() != 1 || strings.Contains(string(data), "hash") {
		t.Fatalf("%s %v", data, err)
	}
	c.opts.MediaSource = func(context.Context, string) (io.ReadCloser, MediaInfo, error) {
		return io.NopCloser(strings.NewReader("not an image")), MediaInfo{Name: "bad.png", ContentType: "image/png", Size: 12}, nil
	}
	_, err = c.uploadProfileImage(context.Background(), "bad-image")
	if codeOf(err) != "INVALID_MEDIA" || uploads.Load() != 1 {
		t.Fatalf("invalid bytes caused network mutation: %v", err)
	}
}
