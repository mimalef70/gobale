package cmd

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/mediafile"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/mimalef70/goomni/src/infrastructure/workpool"
	"io"
	"os"
)

func openProviderMedia(ctx context.Context, d domains.Device, id, root string, store *storage.Store, pool *workpool.Pool, refresh func(context.Context) error) (io.ReadCloser, domains.NativeMediaInfo, error) {
	if err := refresh(ctx); err != nil {
		return nil, domains.NativeMediaInfo{}, err
	}
	release, err := pool.AcquireFor(ctx, d.Provider, d.ConnectionID)
	if err != nil {
		return nil, domains.NativeMediaInfo{}, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	m, err := store.GetMedia(ctx, d.ConnectionID, id)
	if err != nil {
		return nil, domains.NativeMediaInfo{}, err
	}
	path, err := mediafile.Resolve(root, m.Path)
	if err != nil {
		return nil, domains.NativeMediaInfo{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, domains.NativeMediaInfo{}, domains.E("MEDIA_NOT_FOUND", "media unavailable", 404)
	}
	handedOff = true
	return &releaseReader{ReadSeekCloser: file, release: release}, domains.NativeMediaInfo{Name: m.Name, ContentType: m.ContentType, Size: m.Size}, nil
}

// A construction failure is isolated to its selected provider and never becomes
// a client for another messenger. Details stay out of public status and logs.
type unavailableClient struct{}

func (*unavailableClient) Status() domains.ConnectionStatus {
	return domains.ConnectionStatus{Auth: "unauthenticated", Transport: "disconnected", Recovery: "degraded", LastError: "PROVIDER_UNAVAILABLE"}
}
func (*unavailableClient) StartAuth(context.Context, string) (domains.Challenge, error) {
	return domains.Challenge{}, providerUnavailable()
}
func (*unavailableClient) SubmitCode(context.Context, string, string) (*domains.Session, error) {
	return nil, providerUnavailable()
}
func (*unavailableClient) SubmitPassword(context.Context, string, string) (*domains.Session, error) {
	return nil, providerUnavailable()
}
func (*unavailableClient) Connect(context.Context, *domains.Session, domains.Sink) error {
	return providerUnavailable()
}
func (*unavailableClient) Disconnect(context.Context) error { return nil }
func (*unavailableClient) Logout(context.Context) error     { return providerUnavailable() }
func (*unavailableClient) Send(context.Context, domains.SendRequest) (domains.SendResult, error) {
	return domains.SendResult{}, providerUnavailable()
}
func (*unavailableClient) Call(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, providerUnavailable()
}
func providerUnavailable() error {
	return domains.E("PROVIDER_UNAVAILABLE", "provider configuration is unavailable", 503)
}
