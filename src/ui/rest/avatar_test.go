package rest

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

type avatarRESTClient struct {
	testClient
	account string
	calls   *atomic.Int32
}

func (c *avatarRESTClient) DownloadAvatar(_ context.Context, peer domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error) {
	c.calls.Add(1)
	if c.account != "one" || peer.ID == "43" {
		return nil, domains.AvatarInfo{}, domains.E("AVATAR_NOT_FOUND", "no visible avatar", 404)
	}
	return io.NopCloser(strings.NewReader("image-" + size)), domains.AvatarInfo{Name: "avatar.png", ContentType: "image/png", Size: int64(len("image-" + size))}, nil
}

func TestAvatarHTTPAuthScopeBinaryAndSlotRelease(t *testing.T) {
	var calls atomic.Int32
	srv, svc := setupAPIWithFactory(t, "", func(d domains.Device) domains.Client {
		return &avatarRESTClient{account: d.ID, calls: &calls}
	})
	srv.mediaSlots = make(chan struct{}, 1)
	_, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	_, err = svc.CreateDevice(context.Background(), "two")
	require.NoError(t, err)
	for _, tc := range []struct {
		device, query string
		auth          bool
		status        int
	}{
		{"one", "peer=user:42", false, 401}, {"missing", "peer=user:42", true, 404},
		{"one", "peer=group:42", true, 400}, {"one", "peer=user:42&url=https://example.com", true, 400},
		{"one", "peer=user:42&size=full", true, 400},
		{"two", "peer=user:42", true, 404}, {"one", "peer=user:43", true, 404},
		{"one", "peer=user:42", true, 200}, {"one", "peer=user:42&size=large", true, 200},
	} {
		r := httptest.NewRequest("GET", "/user/avatar?"+tc.query, nil)
		if tc.auth {
			r.SetBasicAuth("test", "password")
		}
		r.Header.Set("X-Device-Id", tc.device)
		res, err := srv.App.Test(r)
		require.NoError(t, err)
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		require.Equal(t, tc.status, res.StatusCode, string(body))
		require.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		if tc.status == 200 {
			require.Equal(t, "image/png", res.Header.Get("Content-Type"))
			require.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
			require.Contains(t, res.Header.Get("Content-Disposition"), "inline")
			require.True(t, strings.HasPrefix(string(body), "image-"))
		}
		require.Empty(t, srv.mediaSlots)
	}
	require.EqualValues(t, 4, calls.Load())
}
