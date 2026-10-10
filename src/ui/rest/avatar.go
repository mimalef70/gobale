package rest

import (
	"mime"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/goomni/src/domains"
)

func (s *Server) avatar(c fiber.Ctx) error {
	d, err := s.device(c)
	if err != nil {
		return err
	}
	for key := range c.Queries() {
		if key != "peer" && key != "size" && key != "device_id" {
			return domains.E("INVALID_REQUEST", "unsupported avatar query field", 400)
		}
	}
	parts := strings.SplitN(c.Query("peer"), ":", 2)
	if len(parts) != 2 || parts[0] != "user" {
		return domains.E("INVALID_PEER", "avatar peer must be user:id", 400)
	}
	release, acquireErr := s.acquireMedia(c.Context(), d)
	if acquireErr != nil {
		return acquireErr
	}
	stream := &slotReader{release: release}
	handedOff := false
	defer func() {
		if !handedOff {
			_ = stream.Close()
		}
	}()
	reader, info, err := s.service.DownloadAvatar(c.Context(), d.ID, domains.Peer{Type: parts[0], ID: parts[1]}, c.Query("size", "small"))
	if err != nil {
		return err
	}
	stream.ReadCloser = reader
	limit := min(s.opts.MaxMediaBytes, int64(8<<20))
	if info.Size <= 0 || info.Size > limit {
		return domains.E("AVATAR_TOO_LARGE", "avatar is empty or exceeds configured size limit", 413)
	}
	switch info.ContentType {
	case "image/jpeg", "image/png", "image/gif":
	default:
		return domains.E("AVATAR_INVALID", "provider returned an unsupported image format", 502)
	}
	c.Set("Content-Type", info.ContentType)
	c.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(info.Name)}))
	if err := c.SendStream(stream, int(info.Size)); err != nil {
		return err
	}
	handedOff = true
	return nil
}
