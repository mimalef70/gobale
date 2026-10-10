package rubikameow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"

	"github.com/mimalef70/goomni/src/domains"
)

func avatarUnavailable() error {
	return domains.E("AVATAR_NOT_FOUND", "contact avatar is unavailable", 404)
}
func avatarInvalid() error {
	return domains.E("AVATAR_INVALID", "provider avatar bytes or reference are invalid", 502)
}
func avatarError(err error) error {
	var de *domains.Error
	if errors.As(err, &de) && (de.HTTP == 403 || de.HTTP == 404) {
		return avatarUnavailable()
	}
	return err
}

// getAvatars provides a fresh authenticated main/thumbnail reference. The first
// row is the current avatar, as selected by the reviewed native web client.
// Provider URLs and access hashes are never part of the public response.
func (c *Client) DownloadAvatar(ctx context.Context, user domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error) {
	if size == "" {
		size = "small"
	}
	if size != "small" && size != "large" {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_REQUEST", "avatar size must be small or large", 400)
	}
	if user.Type != "user" || (Contract{}).ValidatePeer(user) != nil {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_PEER", "avatar requires a user peer", 400)
	}
	response, err := c.invoke(ctx, "getAvatars", object{"object_guid": user.ID}, false, "")
	if err != nil {
		return nil, domains.AvatarInfo{}, avatarError(err)
	}
	rows, ok := response["avatars"].([]any)
	if !ok || len(rows) > 100 {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	if len(rows) == 0 {
		return nil, domains.AvatarInfo{}, avatarUnavailable()
	}
	key := "thumbnail"
	if size == "large" {
		key = "main"
	}
	file := asObject(asObject(rows[0])[key])
	if file == nil {
		return nil, domains.AvatarInfo{}, avatarUnavailable()
	}
	id, dc, hash, length := file.str("file_id"), file.str("dc_id"), file.str("access_hash_rec"), file.num("size")
	if !validMessageID(id) || !dcPattern.MatchString(dc) || !validSecret(hash) || length < 1 {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	if length > min(int64(8<<20), c.cfg.MaxMediaBytes) {
		return nil, domains.AvatarInfo{}, domains.E("AVATAR_TOO_LARGE", "contact avatar exceeds the size limit", 413)
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	raw, _ := json.Marshal(privateMedia{Version: 1, Account: account, DC: dc, ID: id, Hash: hash, Size: length})
	// Download constructs the canonical HTTPS data-centre URL from numeric DC only,
	// ignores any provider URL field and never follows redirects.
	stream, err := c.Download(ctx, domains.ProviderMedia{Provider: domains.ProviderRubika, Version: 1, Data: raw, FileID: id, Size: length})
	if err != nil {
		return nil, domains.AvatarInfo{}, avatarError(err)
	}
	defer stream.Close()
	body, err := io.ReadAll(io.LimitReader(stream, length+1))
	if err != nil {
		return nil, domains.AvatarInfo{}, avatarError(err)
	}
	if int64(len(body)) != length {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 16777216 {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	mime := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif"}[format]
	if mime == "" {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	// image.Decode reads only the first GIF frame, with the canvas already bounded.
	decoded, actual, err := image.Decode(bytes.NewReader(body))
	if err != nil || actual != format {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	b := decoded.Bounds()
	if b.Empty() || b.Min.X < 0 || b.Min.Y < 0 || b.Max.X > cfg.Width || b.Max.Y > cfg.Height || (format != "gif" && (b.Dx() != cfg.Width || b.Dy() != cfg.Height)) {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	extension := format
	if extension == "jpeg" {
		extension = "jpg"
	}
	return io.NopCloser(bytes.NewReader(body)), domains.AvatarInfo{Name: "avatar." + extension, ContentType: mime, Size: length, Width: cfg.Width, Height: cfg.Height}, nil
}

var _ domains.AvatarDownloader = (*Client)(nil)
