package balemeow

import (
	"bytes"
	"context"
	"image"
	"io"
	"math"
	"mime"
	"path/filepath"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
)

// uploadProfileImage reads only the gateway's account-scoped media store.
// Profile uploads have no conversation ExPeer; arbitrary remote URLs or file
// access hashes are never accepted from the API caller.
func (c *Client) uploadProfileImage(ctx context.Context, mediaID string) (*wire.FileLocation, error) {
	location, _, err := c.uploadNativeImage(ctx, mediaID, nil)
	return location, err
}

// Stories use the same scoped file upload with PHOTO send type and no peer.
func (c *Client) uploadNativeImage(ctx context.Context, mediaID string, sendType *wire.SendType) (*wire.FileLocation, int64, error) {
	if mediaID == "" || len(mediaID) > 128 {
		return nil, 0, groupRequestError("media_id is required")
	}
	if c.opts.MediaSource == nil {
		return nil, 0, domains.Unsupported("media source")
	}
	source, info, err := c.opts.MediaSource(ctx, mediaID)
	if err != nil {
		return nil, 0, err
	}
	if source == nil {
		return nil, 0, boundedError("MEDIA_NOT_FOUND", "media source is unavailable", 404)
	}
	defer source.Close()
	if info.Size <= 0 || info.Size > c.opts.MaxMediaBytes || info.Size > math.MaxInt32 {
		return nil, 0, boundedError("MEDIA_TOO_LARGE", "image is empty or exceeds configured media limit", 413)
	}
	name := filepath.Base(info.Name)
	if name == "" || name == "." || len(name) > 255 || strings.ContainsAny(name, "\x00\r\n") {
		return nil, 0, boundedError("INVALID_MEDIA", "image name is invalid", 400)
	}
	contentType, _, err := mime.ParseMediaType(info.ContentType)
	if err != nil {
		return nil, 0, boundedError("INVALID_MEDIA", "image content type is invalid", 400)
	}
	var prefix bytes.Buffer
	cfg, format, err := image.DecodeConfig(io.TeeReader(io.LimitReader(source, 1<<20), &prefix))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 32768 || cfg.Height > 32768 {
		return nil, 0, boundedError("INVALID_MEDIA", "image header or dimensions are invalid", 400)
	}
	canonical := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif"}[format]
	if canonical == "" || contentType != canonical {
		return nil, 0, boundedError("INVALID_MEDIA", "image content type does not match its bytes", 400)
	}
	c.mu.Lock()
	account := ""
	if c.session != nil {
		account = c.session.UserID
	}
	c.mu.Unlock()
	uid, err := positiveID(account)
	if err != nil {
		return nil, 0, unavailable()
	}
	data, err := c.rpc(ctx, "ai.bale.server.Files", "GetNasimFileUploadUrl", &wire.FileUploadRequest{Uid: uid, ExpectedSize: int32(info.Size), Name: name, MimeType: canonical, SendType: sendType})
	if err != nil {
		return nil, 0, mediaSetupError(err)
	}
	reply := &wire.FileUploadResponse{}
	if decode(data, reply) != nil || reply.FileId == 0 {
		return nil, 0, boundedError("MEDIA_UPLOAD_RESPONSE_INVALID", "provider upload response has no valid file reference", 502)
	}
	if !reply.Duplicate {
		if err := c.uploadBytes(ctx, reply.Url, io.MultiReader(bytes.NewReader(prefix.Bytes()), source), info.Size); err != nil {
			return nil, 0, err
		}
	}
	return &wire.FileLocation{FileId: reply.FileId, AccessHash: uid}, info.Size, nil
}
