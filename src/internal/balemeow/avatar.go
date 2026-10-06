package balemeow

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"net/http"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

const maxAvatarBytes int64 = 8 << 20

// DownloadAvatar resolves the user through the selected authenticated account,
// and fetches its current avatar anew so privacy/photo changes are respected.
// Provider file capabilities and signed URLs never cross this boundary. Unlike
// large message files, avatars are small and fully bounded before returning any
// bytes, allowing the caller to return a reliable error instead of a partial 200.
func (c *Client) DownloadAvatar(ctx context.Context, user domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error) {
	if size == "" {
		size = "small"
	}
	if size != "small" && size != "large" {
		return nil, domains.AvatarInfo{}, boundedError("INVALID_REQUEST", "avatar size must be small or large", 400)
	}
	if user.Type != "user" || user.AccessHash != "" {
		return nil, domains.AvatarInfo{}, boundedError("INVALID_PEER", "avatar requires a user peer without an access hash", 400)
	}
	lookup, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	ref, err := c.accountUserRef(lookup, user)
	if err != nil {
		cancel()
		return nil, domains.AvatarInfo{}, avatarProviderError(err)
	}
	data, err := c.readRPC(lookup, accountUsersService, "GetFullUser", &wire.GetUserInfoRequest{Peer: ref})
	cancel()
	if err != nil {
		return nil, domains.AvatarInfo{}, avatarProviderError(err)
	}
	response := &wire.UserAvatarResponse{}
	if decode(data, response) != nil || response.User == nil || response.User.Id != ref.Id {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	if response.User.Avatar == nil {
		return nil, domains.AvatarInfo{}, avatarNotFound()
	}
	variants := []*wire.AvatarImage{response.User.Avatar.Small, response.User.Avatar.Large, response.User.Avatar.Full}
	if size == "large" {
		variants = []*wire.AvatarImage{response.User.Avatar.Large, response.User.Avatar.Full, response.User.Avatar.Small}
	}
	var selected *wire.AvatarImage
	for _, variant := range variants {
		if variant != nil {
			selected = variant
			break
		}
	}
	if selected == nil {
		return nil, domains.AvatarInfo{}, avatarNotFound()
	}
	limit := maxAvatarBytes
	if c.opts.MaxMediaBytes < limit {
		limit = c.opts.MaxMediaBytes
	}
	if selected.File == nil || selected.File.FileId == 0 || selected.FileSize < 0 || selected.Width < 0 || selected.Height < 0 {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	if int64(selected.FileSize) > limit {
		return nil, domains.AvatarInfo{}, avatarTooLarge()
	}
	body, err := c.downloadAvatarBytes(ctx, selected.File, limit)
	if err != nil {
		return nil, domains.AvatarInfo{}, err
	}
	if selected.FileSize > 0 && int64(len(body)) != int64(selected.FileSize) {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 16<<20 {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	contentType := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif"}[format]
	if contentType == "" {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	// Decode after the dimension limit so a valid header followed by corrupt or
	// truncated pixels cannot become a successful avatar response. For GIF this
	// validates the first frame, without allocating the entire animation. A
	// decoded raster is bounded to 16 M pixels (at most 128 MiB for RGBA64).
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(body))
	if err != nil || decodedFormat != format {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	bounds := decoded.Bounds()
	// A GIF frame may cover only part of its logical screen, while PNG/JPEG
	// must decode to the exact header dimensions.
	if bounds.Empty() || bounds.Min.X < 0 || bounds.Min.Y < 0 || bounds.Max.X > cfg.Width || bounds.Max.Y > cfg.Height || (format != "gif" && (bounds.Dx() != cfg.Width || bounds.Dy() != cfg.Height)) {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	// Metadata is derived from the downloaded bytes, not upstream HTTP headers or
	// a provider-controlled filename. No user identifiers are included in names.
	ext := format
	if ext == "jpeg" {
		ext = "jpg"
	}
	info := domains.AvatarInfo{Name: "avatar." + ext, ContentType: contentType, Size: int64(len(body)), Width: cfg.Width, Height: cfg.Height}
	return io.NopCloser(bytes.NewReader(body)), info, nil
}

func (c *Client) downloadAvatarBytes(ctx context.Context, file *wire.FileLocation, limit int64) ([]byte, error) {
	lookup, cancelLookup := context.WithTimeout(ctx, c.opts.RequestTimeout)
	data, err := c.readRPC(lookup, "ai.bale.server.Files", "GetNasimFileUrl", &wire.FileDownloadRequest{File: file})
	cancelLookup()
	if err != nil {
		return nil, avatarProviderError(err)
	}
	reply := &wire.FileDownloadResponse{}
	if decode(data, reply) != nil || reply.FileUrl == nil || reply.FileUrl.FileId != file.FileId || reply.FileUrl.Algorithm != "" || len(reply.FileUrl.FileKey) != 0 || len(reply.FileUrl.Nonce) != 0 {
		return nil, avatarInvalid()
	}
	u, err := c.validateMediaURL(reply.FileUrl.Url)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithTimeout(ctx, c.opts.MediaTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(lifetime, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, avatarInvalid()
	}
	response, err := c.mediaHTTP().Do(request)
	if err != nil {
		return nil, boundedError("AVATAR_DOWNLOAD_FAILED", "avatar download did not complete", 502)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusForbidden {
		return nil, avatarNotFound()
	}
	if response.StatusCode != http.StatusOK {
		return nil, boundedError("AVATAR_DOWNLOAD_FAILED", "provider rejected avatar download", 502)
	}
	if response.ContentLength > limit {
		return nil, avatarTooLarge()
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if int64(len(body)) > limit {
		return nil, avatarTooLarge()
	}
	if err != nil {
		return nil, boundedError("AVATAR_DOWNLOAD_FAILED", "avatar download did not complete", 502)
	}
	return body, nil
}

func avatarProviderError(err error) error {
	var de *domains.Error
	if errors.As(err, &de) && (de.Code == "PROVIDER_NOT_FOUND" || de.Code == "PROVIDER_PERMISSION_DENIED") {
		return avatarNotFound()
	}
	return err
}
func avatarNotFound() error {
	return boundedError("AVATAR_NOT_FOUND", "no avatar is available to this account", 404)
}
func avatarInvalid() error {
	return boundedError("AVATAR_INVALID", "provider returned invalid avatar data", 502)
}
func avatarTooLarge() error {
	return boundedError("AVATAR_TOO_LARGE", "avatar exceeds the download size limit", 413)
}
