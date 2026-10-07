package rest

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/mimalef70/gobale/src/usecase"
	"github.com/stretchr/testify/require"
)

type mediaClient struct {
	testClient
	calls atomic.Int32
}

func (m *mediaClient) Download(ctx context.Context, ref domains.ProviderMedia) (io.ReadCloser, error) {
	m.calls.Add(1)
	return io.NopCloser(strings.NewReader("fixture")), nil
}

func TestRemoteDownloadUsesScopedReferenceAndReleasesSlot(t *testing.T) {
	dir := t.TempDir()
	st, err := storage.Open(filepath.Join(dir, "test.db"), bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	client := &mediaClient{}
	svc := usecase.New(st, usecase.Options{}, func(domains.Device) domains.Client { return client })
	require.NoError(t, svc.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, svc.Close(ctx))
		require.NoError(t, st.Close())
	})
	one, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	_, err = svc.CreateDevice(context.Background(), "two")
	require.NoError(t, err)
	peer := domains.Peer{Type: "user", ID: "123"}
	require.NoError(t, st.SaveProviderMedia(context.Background(), one.ConnectionID, peer, "456", domains.ProviderMedia{FileID: "-789", AccessHash: "7987654321", Size: 7, Name: "test.txt", ContentType: "text/plain"}))
	srv, err := New(svc, st, Options{BasicAuth: "test:password", MediaSlots: make(chan struct{}, 1), MaxMediaBytes: 1024})
	require.NoError(t, err)
	for _, device := range []string{"two", "one", "one"} {
		r := httptest.NewRequest("GET", "/message/456/download?peer=user:123", nil)
		r.SetBasicAuth("test", "password")
		r.Header.Set("X-Device-Id", device)
		scopeTestRequest(t, srv, r)
		res, err := srv.App.Test(r)
		require.NoError(t, err)
		b, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		if device == "two" {
			require.Equal(t, 404, res.StatusCode)
			require.Zero(t, client.calls.Load())
		} else {
			require.Equal(t, 200, res.StatusCode)
			require.Equal(t, "fixture", string(b))
			require.Contains(t, res.Header.Get("Content-Disposition"), "test.txt")
		}
		require.NotContains(t, string(b), "7987654321")
	}
	require.EqualValues(t, 2, client.calls.Load())
}
