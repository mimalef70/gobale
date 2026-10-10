package balemeow

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
)

func avatarPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, image.NewNRGBA(image.Rect(0, 0, 2, 3))))
	return out.Bytes()
}

func avatarWireResponse(size int) *wire.UserAvatarResponse {
	return &wire.UserAvatarResponse{User: &wire.UserAvatarProjection{Id: 42, Avatar: &wire.AvatarImageSet{Small: &wire.AvatarImage{File: &wire.FileLocation{FileId: -987654321, AccessHash: -654321}, Width: 2, Height: 3, FileSize: int32(size)}}}}
}

func avatarClient(t *testing.T, response *wire.UserAvatarResponse, handler http.HandlerFunc) (*Client, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	httpServer := httptest.NewTLSServer(handler)
	t.Cleanup(httpServer.Close)
	var lookups, urls atomic.Int32
	var f *fakeWS
	f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		switch m.Request.Method {
		case "GetFullUser":
			lookups.Add(1)
			require.Equal(t, accountUsersService, m.Request.Service)
			var p wire.GetUserInfoRequest
			require.NoError(t, decode(m.Request.Payload, &p))
			require.Equal(t, uint32(42), p.Peer.Id)
			require.Equal(t, int64(998877), p.Peer.AccessHash)
			respond(t, f, ws, m.Request, response)
		case "GetNasimFileUrl":
			urls.Add(1)
			var p wire.FileDownloadRequest
			require.NoError(t, decode(m.Request.Payload, &p))
			require.Equal(t, int64(-654321), p.File.AccessHash)
			respond(t, f, ws, m.Request, &wire.FileDownloadResponse{FileUrl: &wire.FileURL{FileId: p.File.FileId, Url: httpServer.URL + "/avatar?capability=private-synthetic"}})
		default:
			t.Errorf("unexpected avatar RPC %s", m.Request.Method)
		}
	})
	c := mediaClient(t, f, httpServer, nil, MediaInfo{})
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 998877})
	return c, &lookups, &urls
}

func TestAvatarDownloadsFreshImageWithoutLeakingCapabilities(t *testing.T) {
	body := avatarPNG(t)
	c, lookups, urls := avatarClient(t, avatarWireResponse(len(body)), func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		require.Empty(t, r.Header.Get("Token"))
		// Neither a misleading MIME header nor filename may influence the API.
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Disposition", `attachment; filename="private-token.html"`)
		_, _ = w.Write(body)
	})
	for i := 0; i < 2; i++ {
		r, meta, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "42"}, "")
		require.NoError(t, err)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		require.Equal(t, body, got)
		require.Equal(t, domains.AvatarInfo{Name: "avatar.png", ContentType: "image/png", Size: int64(len(body)), Width: 2, Height: 3}, meta)
	}
	require.Equal(t, int32(2), lookups.Load(), "never reuse an avatar across privacy/photo changes")
	require.Equal(t, int32(2), urls.Load())
}

func TestAvatarValidatesBeforeNetwork(t *testing.T) {
	cases := []struct {
		peer domains.Peer
		size string
		code string
	}{
		{domains.Peer{Type: "user", ID: "42"}, "full", "INVALID_REQUEST"},
		{domains.Peer{Type: "group", ID: "42"}, "", "INVALID_PEER"},
		{domains.Peer{Type: "user", ID: "42", AccessHash: "77"}, "", "INVALID_PEER"},
		{domains.Peer{Type: "user", ID: "bad"}, "", "INVALID_PEER"},
	}
	for _, tc := range cases {
		c := New(Options{})
		body, info, err := c.DownloadAvatar(context.Background(), tc.peer, tc.size)
		require.Equal(t, tc.code, codeOf(err))
		require.Nil(t, body)
		require.Zero(t, info)
	}
}

func TestAvatarRejectsUnavailableOrMalformedReferencesBeforeDownload(t *testing.T) {
	cases := []struct {
		name   string
		change func(*wire.UserAvatarResponse)
		code   string
	}{
		{"missing avatar", func(r *wire.UserAvatarResponse) { r.User.Avatar = nil }, "AVATAR_NOT_FOUND"},
		{"missing variants", func(r *wire.UserAvatarResponse) { r.User.Avatar = &wire.AvatarImageSet{} }, "AVATAR_NOT_FOUND"},
		{"wrong user", func(r *wire.UserAvatarResponse) { r.User.Id = 99 }, "AVATAR_INVALID"},
		{"missing user", func(r *wire.UserAvatarResponse) { r.User = nil }, "AVATAR_INVALID"},
		{"missing file", func(r *wire.UserAvatarResponse) { r.User.Avatar.Small.File = nil }, "AVATAR_INVALID"},
		{"zero file", func(r *wire.UserAvatarResponse) { r.User.Avatar.Small.File.FileId = 0 }, "AVATAR_INVALID"},
		{"negative size", func(r *wire.UserAvatarResponse) { r.User.Avatar.Small.FileSize = -1 }, "AVATAR_INVALID"},
		{"oversize", func(r *wire.UserAvatarResponse) { r.User.Avatar.Small.FileSize = 9 << 20 }, "AVATAR_TOO_LARGE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := avatarWireResponse(100)
			tc.change(reply)
			c, _, urls := avatarClient(t, reply, func(http.ResponseWriter, *http.Request) { t.Error("invalid avatar reached HTTP") })
			body, info, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "42"}, "small")
			require.Equal(t, tc.code, codeOf(err))
			require.Nil(t, body)
			require.Zero(t, info)
			require.Zero(t, urls.Load())
		})
	}
}

func TestAvatarSizeSelectionAndFallback(t *testing.T) {
	cases := []struct {
		size, available string
		want            int64
	}{
		{"", "all", 1}, {"small", "all", 1}, {"large", "all", 2},
		{"small", "large", 2}, {"large", "full", 3}, {"large", "small", 1},
	}
	body := avatarPNG(t)
	for _, tc := range cases {
		t.Run(tc.size+"/"+tc.available, func(t *testing.T) {
			tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
			defer tls.Close()
			variants := &wire.AvatarImageSet{}
			image := func(id int64) *wire.AvatarImage {
				return &wire.AvatarImage{File: &wire.FileLocation{FileId: id}, FileSize: int32(len(body))}
			}
			if tc.available == "all" || tc.available == "small" {
				variants.Small = image(1)
			}
			if tc.available == "all" || tc.available == "large" {
				variants.Large = image(2)
			}
			if tc.available == "all" || tc.available == "full" {
				variants.Full = image(3)
			}
			var f *fakeWS
			f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				if m.Request.Method == "GetFullUser" {
					respond(t, f, ws, m.Request, &wire.UserAvatarResponse{User: &wire.UserAvatarProjection{Id: 42, Avatar: variants}})
				} else {
					require.Equal(t, "GetNasimFileUrl", m.Request.Method)
					var p wire.FileDownloadRequest
					require.NoError(t, decode(m.Request.Payload, &p))
					require.Equal(t, tc.want, p.File.FileId)
					respond(t, f, ws, m.Request, &wire.FileDownloadResponse{FileUrl: &wire.FileURL{FileId: p.File.FileId, Url: tls.URL}})
				}
			})
			c := mediaClient(t, f, tls, nil, MediaInfo{})
			c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 998877})
			r, _, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "42"}, tc.size)
			require.NoError(t, err)
			require.NoError(t, r.Close())
		})
	}
}

func TestAvatarDownloadRejectsBadContentAndOversizeBeforeReturningBody(t *testing.T) {
	cases := []struct {
		name     string
		body     []byte
		declared int
		status   int
		limit    int64
		code     string
	}{
		{"html", []byte("<html>secret</html>"), 0, 200, 1024, "AVATAR_INVALID"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0, 200, 1024, "AVATAR_INVALID"},
		{"missing", nil, 0, 404, 1024, "AVATAR_NOT_FOUND"},
		{"private", nil, 0, 403, 1024, "AVATAR_NOT_FOUND"},
		{"upstream error", nil, 0, 500, 1024, "AVATAR_DOWNLOAD_FAILED"},
		{"size mismatch", avatarPNG(t), 7, 200, 1024, "AVATAR_INVALID"},
		{"valid header truncated pixels", avatarPNG(t)[:33], 0, 200, 1024, "AVATAR_INVALID"},
		{"chunked oversize", bytes.Repeat([]byte{1}, 257), 0, 200, 256, "AVATAR_TOO_LARGE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := avatarClient(t, avatarWireResponse(tc.declared), func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.(http.Flusher).Flush() // exercise streaming size limit without Content-Length
				_, _ = w.Write(tc.body)
			})
			c.opts.MaxMediaBytes = tc.limit
			r, meta, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "42"}, "")
			require.Equal(t, tc.code, codeOf(err))
			require.Nil(t, r)
			require.Zero(t, meta)
		})
	}
}

func TestAvatarRedirectDoesNotReachAnotherDestination(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	c, _, _ := avatarClient(t, avatarWireResponse(0), func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	})
	r, _, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "42"}, "")
	require.Equal(t, "AVATAR_DOWNLOAD_FAILED", codeOf(err))
	require.Nil(t, r)
	require.Zero(t, hits.Load())
}

func TestAvatarCancelledDownloadStopsHTTP(t *testing.T) {
	started := make(chan struct{})
	ended := make(chan struct{})
	c, _, _ := avatarClient(t, avatarWireResponse(0), func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(ended)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := c.DownloadAvatar(ctx, domains.Peer{Type: "user", ID: "42"}, ""); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("download did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.Equal(t, "AVATAR_DOWNLOAD_FAILED", codeOf(err))
	case <-time.After(time.Second):
		t.Fatal("download was not cancelled")
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("HTTP remained active")
	}
}

func TestAvatarURLMustBeAllowlistedAndProviderErrorsAreSanitized(t *testing.T) {
	c := New(Options{})
	for _, raw := range []string{"http://file-gw1.ble.ir/a", "https://evil.example/a", "https://file-gw1.ble.ir.evil.example/a", "https://user:password@file-gw1.ble.ir/a"} {
		_, err := c.validateMediaURL(raw)
		require.Equal(t, "UNTRUSTED_MEDIA_URL", codeOf(err))
		require.False(t, strings.Contains(err.Error(), raw))
	}
	for _, code := range []string{"PROVIDER_NOT_FOUND", "PROVIDER_PERMISSION_DENIED"} {
		err := avatarProviderError(&domains.Error{Code: code, Message: "private provider body", HTTP: 403})
		require.Equal(t, "AVATAR_NOT_FOUND", codeOf(err))
		require.NotContains(t, err.Error(), "private")
	}
}

func TestAvatarWireProjectionUsesReviewedFieldNumbers(t *testing.T) {
	// Independently encoded literal fixture: response1 -> FullUser(id1,avatar6)
	// -> avatar small1 -> image(file1,width2,height3,size4) -> location(id1,hash2).
	// This must not use the types under test to build its expected wire layout.
	raw := []byte{0x0a, 0x12, 0x08, 0x2a, 0x32, 0x0e, 0x0a, 0x0c, 0x0a, 0x04, 0x08, 0x65, 0x10, 0x66, 0x10, 0x02, 0x18, 0x03, 0x20, 0x64}
	var response wire.UserAvatarResponse
	require.NoError(t, decode(raw, &response))
	require.Equal(t, uint32(42), response.User.Id)
	require.Equal(t, int64(101), response.User.Avatar.Small.File.FileId)
	require.Equal(t, int64(102), response.User.Avatar.Small.File.AccessHash)
	require.Equal(t, int32(2), response.User.Avatar.Small.Width)
	require.Equal(t, int32(3), response.User.Avatar.Small.Height)
	require.Equal(t, int32(100), response.User.Avatar.Small.FileSize)
}
