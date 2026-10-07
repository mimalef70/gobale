package rest

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

type panicTransferClient struct {
	testClient
	panicking atomic.Bool
	contexts  chan context.Context
	closed    atomic.Int32
}

func (p *panicTransferClient) reader(ctx context.Context) io.ReadCloser {
	p.contexts <- ctx
	if p.panicking.Load() {
		panic("private-provider-token")
	}
	return &transferTestReader{Context: ctx, Reader: strings.NewReader("fixture"), closed: &p.closed}
}
func (p *panicTransferClient) Download(ctx context.Context, _ domains.ProviderMedia) (io.ReadCloser, error) {
	return p.reader(ctx), nil
}
func (p *panicTransferClient) DownloadAvatar(ctx context.Context, _ domains.Peer, _ string) (io.ReadCloser, domains.AvatarInfo, error) {
	return p.reader(ctx), domains.AvatarInfo{Name: "fixture.png", ContentType: "image/png", Size: 7}, nil
}

type transferTestReader struct {
	context.Context
	io.Reader
	closed *atomic.Int32
}

func (r *transferTestReader) Read(b []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(b)
}
func (r *transferTestReader) Close() error { r.closed.Add(1); return nil }

func TestMediaProviderPanicReleasesSlotAndContextBeforeNextTransfer(t *testing.T) {
	for _, path := range []string{"/message/456/download?peer=user:123", "/user/avatar?peer=user:123"} {
		t.Run(path, func(t *testing.T) {
			client := &panicTransferClient{contexts: make(chan context.Context, 3)}
			s, svc := setupAPIWithFactory(t, "/gateway", func(domains.Device) domains.Client { return client })
			s.mediaSlots = make(chan struct{}, 1)
			s.opts.RequestTimeout = time.Nanosecond // Streams must own their lifetime.
			d, err := svc.CreateDevice(context.Background(), "synthetic")
			require.NoError(t, err)
			require.NoError(t, s.store.SaveProviderMedia(context.Background(), d.ConnectionID, domains.Peer{Type: "user", ID: "123"}, "456", domains.ProviderMedia{FileID: "-789", AccessHash: "-987", Size: 7, Name: "fixture.png", ContentType: "image/png"}))
			for index := 0; index < 3; index++ {
				client.panicking.Store(index == 0)
				req := httptest.NewRequest("GET", "/gateway"+path, nil)
				req.SetBasicAuth("test", "password")
				req.Header.Set("X-Device-Id", d.ID)
				scopeTestRequest(t, s, req)
				res, err := s.App.Test(req)
				require.NoError(t, err)
				body, err := io.ReadAll(res.Body)
				require.NoError(t, err)
				require.NoError(t, res.Body.Close())
				if index == 0 {
					require.Equal(t, 500, res.StatusCode)
					require.Contains(t, string(body), "INTERNAL_SERVER_ERROR")
					require.NotContains(t, string(body), "private-provider-token")
				} else {
					require.Equal(t, 200, res.StatusCode)
					require.Equal(t, "fixture", string(body))
				}
				require.Empty(t, s.mediaSlots)
				providerContext := <-client.contexts
				_, deadline := providerContext.Deadline()
				require.False(t, deadline)
				require.ErrorIs(t, providerContext.Err(), context.Canceled)
				require.EqualValues(t, index, client.closed.Load(), "each handed-off stream closes exactly once")
			}
		})
	}
}
