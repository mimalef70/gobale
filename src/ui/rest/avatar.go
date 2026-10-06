package rest

import (
	"mime"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
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
	select {
	case s.mediaSlots <- struct{}{}:
	default:
		return domains.E("MEDIA_BUSY", "media transfer capacity reached", 503)
	}
	reader, info, err := s.service.DownloadAvatar(c.Context(), d.ID, domains.Peer{Type: parts[0], ID: parts[1]}, c.Query("size", "small"))
	if err != nil {
		<-s.mediaSlots
		return err
	}
	limit := min(s.opts.MaxMediaBytes, int64(8<<20))
	if info.Size <= 0 || info.Size > limit {
		_ = reader.Close()
		<-s.mediaSlots
		return domains.E("AVATAR_TOO_LARGE", "avatar is empty or exceeds configured size limit", 413)
	}
	switch info.ContentType {
	case "image/jpeg", "image/png", "image/gif":
	default:
		_ = reader.Close()
		<-s.mediaSlots
		return domains.E("AVATAR_INVALID", "provider returned an unsupported image format", 502)
	}
	c.Set("Content-Type", info.ContentType)
	c.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(info.Name)}))
	stream := &slotReader{ReadCloser: reader, release: func() { <-s.mediaSlots }}
	if err := c.SendStream(stream, int(info.Size)); err != nil {
		_ = stream.Close()
		return err
	}
	return nil
}
