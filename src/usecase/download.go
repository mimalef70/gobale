package usecase

import (
	"context"
	"io"
	"sync"

	"github.com/mimalef70/goomni/src/domains"
)

// Download accepts only an account-scoped message reference. Provider file IDs
// and access hashes are loaded from encrypted storage, never from the caller.
// The returned reader owns its child context until Close; the HTTP adapter must
// close it even if the consuming client disconnects early.
func (s *Service) Download(ctx context.Context, id string, peer domains.Peer, messageID string) (io.ReadCloser, domains.ProviderMedia, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	contract, err := s.provider(d.Provider)
	if err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	validID := domains.ValidOpaqueID(messageID)
	if identity, ok := contract.(domains.MessageIdentityContract); ok {
		validID = validID && identity.ValidateMessageID(messageID)
	}
	if !validID {
		return nil, domains.ProviderMedia{}, domains.E("INVALID_MESSAGE_ID", "message_id is invalid for this provider", 400)
	}
	if err = contract.ValidatePeer(peer); err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	descriptor, err := s.store.GetProviderMedia(ctx, d.ConnectionID, peer, messageID)
	if err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	e, err := s.entry(d)
	if err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	// Capturing the provider under a short pointer lock avoids holding the
	// account's lifecycle/send lock during file transfer. Its connection identity
	// can never be rebound; concurrent logout may abort this request, not reroute it.
	e.clientMu.RLock()
	client := e.client
	e.clientMu.RUnlock()
	downloader, ok := client.(domains.MediaDownloader)
	if !ok {
		return nil, domains.ProviderMedia{}, domains.Unsupported("provider media download")
	}
	lifetime, cancel := context.WithCancel(ctx)
	s.mu.RLock()
	serviceContext := s.ctx
	s.mu.RUnlock()
	stopService := func() bool { return false }
	if serviceContext != nil {
		stopService = context.AfterFunc(serviceContext, cancel)
	}
	release := func() { stopService(); cancel() }
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	reader, err := downloader.Download(lifetime, descriptor)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, domains.ProviderMedia{}, safeError(err)
	}
	if reader == nil {
		return nil, domains.ProviderMedia{}, domains.E("INVALID_PROVIDER_MEDIA", "provider returned no media stream", 502)
	}
	handedOff = true
	return &cancelledReader{ReadCloser: reader, cancel: release}, descriptor, nil
}

type cancelledReader struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

func (r *cancelledReader) Close() error {
	r.once.Do(func() { r.cancel(); r.err = r.ReadCloser.Close() })
	return r.err
}
