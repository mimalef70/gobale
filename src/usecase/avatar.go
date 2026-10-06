package usecase

import (
	"context"
	"io"

	"github.com/mimalef70/gobale/src/domains"
)

// DownloadAvatar selects an immutable account client before resolving the
// contact image. The caller cannot supply a provider URL, file ID or hash.
func (s *Service) DownloadAvatar(ctx context.Context, id string, user domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, domains.AvatarInfo{}, err
	}
	if user.AccessHash != "" {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_PEER", "caller-supplied access hashes are not accepted", 400)
	}
	if err = validMutationPeer(user, "user", false); err != nil {
		return nil, domains.AvatarInfo{}, err
	}
	if size == "" {
		size = "small"
	}
	if size != "small" && size != "large" {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_REQUEST", "avatar size must be small or large", 400)
	}
	e, err := s.entry(d)
	if err != nil {
		return nil, domains.AvatarInfo{}, err
	}
	e.clientMu.RLock()
	client := e.client
	e.clientMu.RUnlock()
	downloader, ok := client.(domains.AvatarDownloader)
	if !ok {
		return nil, domains.AvatarInfo{}, domains.Unsupported("contact avatar download")
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
	reader, info, err := downloader.DownloadAvatar(lifetime, user, size)
	if err != nil {
		release()
		if reader != nil {
			_ = reader.Close()
		}
		return nil, domains.AvatarInfo{}, safeError(err)
	}
	if reader == nil {
		release()
		return nil, domains.AvatarInfo{}, domains.E("AVATAR_INVALID", "provider returned no avatar stream", 502)
	}
	return &cancelledReader{ReadCloser: reader, cancel: release}, info, nil
}
