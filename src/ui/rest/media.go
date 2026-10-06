package rest

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/mediafile"
)

func (s *Server) upload(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	select {
	case s.mediaSlots <- struct{}{}:
		defer func() { <-s.mediaSlots }()
	default:
		return domains.E("MEDIA_BUSY", "media transfer capacity reached", 503)
	}
	var body io.Reader = c.Request().BodyStream()
	if body == nil {
		body = strings.NewReader(string(c.Body()))
	}
	return s.saveUpload(c, d, body, c.Get("X-Filename", "upload.bin"), c.Get("Content-Type", "application/octet-stream"))
}
func (s *Server) saveUpload(c fiber.Ctx, d domains.Device, body io.Reader, name, contentType string) error {
	root, e := filepath.Abs(s.opts.MediaRoot)
	if e != nil {
		return e
	}
	dir := filepath.Join(root, d.ConnectionID)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".upload-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	n, e := io.Copy(f, io.LimitReader(body, s.opts.MaxMediaBytes+1))
	syncErr := f.Sync()
	ce := f.Close()
	if syncErr != nil {
		return syncErr
	}
	if e != nil {
		return domains.E("MEDIA_UPLOAD_FAILED", "incomplete upload", 400)
	}
	if ce != nil {
		return ce
	}
	if n > s.opts.MaxMediaBytes {
		return domains.E("MEDIA_TOO_LARGE", "media exceeds configured limit", 413)
	}
	id := uuid.NewString()
	path := filepath.Join(dir, id)
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	dirHandle, e := os.Open(dir)
	if e != nil {
		_ = os.Remove(path)
		return e
	}
	syncErr = dirHandle.Sync()
	_ = dirHandle.Close()
	if syncErr != nil {
		_ = os.Remove(path)
		return syncErr
	}
	m := domains.Media{ID: id, ConnectionID: d.ConnectionID, Name: filepath.Base(name), ContentType: contentType, Size: n, Path: filepath.ToSlash(filepath.Join(d.ConnectionID, id)), CreatedAt: time.Now().UTC()}
	if e = s.store.SaveMedia(c.Context(), m); e != nil {
		_ = os.Remove(path)
		return e
	}
	return c.Status(201).JSON(map[string]any{"code": "SUCCESS", "message": "Media stored locally", "results": m})
}
func (s *Server) download(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	m, e := s.store.GetMedia(c.Context(), d.ConnectionID, c.Params("media_id"))
	if e != nil {
		return e
	}
	absPath, e := mediafile.Resolve(s.opts.MediaRoot, m.Path)
	if e != nil {
		return e
	}
	select {
	case s.mediaSlots <- struct{}{}:
	default:
		return domains.E("MEDIA_BUSY", "media transfer capacity reached", 503)
	}
	f, e := os.Open(absPath)
	if e != nil {
		<-s.mediaSlots
		return domains.E("MEDIA_NOT_FOUND", "media file unavailable", 404)
	}
	c.Set("Content-Type", m.ContentType)
	c.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": m.Name}))
	c.Set("X-Content-Type-Options", "nosniff")
	stream := &slotReader{ReadCloser: f, release: func() { <-s.mediaSlots }}
	if err := c.SendStream(stream, int(m.Size)); err != nil {
		_ = stream.Close()
		return err
	}
	return nil
}
func (s *Server) downloadMessage(c fiber.Ctx) error {
	d, err := s.device(c)
	if err != nil {
		return err
	}
	parts := strings.SplitN(c.Query("peer"), ":", 2)
	if len(parts) != 2 {
		return domains.E("INVALID_PEER", "peer query must be type:id", 400)
	}
	peer := domains.Peer{Type: parts[0], ID: parts[1]}
	if err = peer.Validate(); err != nil {
		return err
	}
	select {
	case s.mediaSlots <- struct{}{}:
	default:
		return domains.E("MEDIA_BUSY", "media transfer capacity reached", 503)
	}
	reader, media, err := s.service.Download(c.Context(), d.ID, peer, c.Params("message_id"))
	if err != nil {
		<-s.mediaSlots
		return err
	}
	if media.Size < 0 || media.Size > s.opts.MaxMediaBytes {
		_ = reader.Close()
		<-s.mediaSlots
		return domains.E("MEDIA_TOO_LARGE", "media exceeds configured limit", 413)
	}
	c.Set("Content-Type", media.ContentType)
	c.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(media.Name)}))
	c.Set("X-Content-Type-Options", "nosniff")
	stream := &slotReader{ReadCloser: reader, release: func() { <-s.mediaSlots }}
	if err = c.SendStream(stream, int(media.Size)); err != nil {
		_ = stream.Close()
		return err
	}
	return nil
}
func (s *Server) fetchMedia(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	var req struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if e = decode(c, &req); e != nil {
		return e
	}
	u, e := url.Parse(req.URL)
	if e != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return domains.E("INVALID_MEDIA_URL", "an HTTP(S) URL without credentials is required", 400)
	}
	select {
	case s.mediaSlots <- struct{}{}:
		defer func() { <-s.mediaSlots }()
	default:
		return domains.E("MEDIA_BUSY", "media transfer capacity reached", 503)
	}
	transport := &http.Transport{Proxy: nil, DialContext: safeMediaDial, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, e := http.NewRequestWithContext(c.Context(), "GET", u.String(), nil)
	if e != nil {
		return domains.E("INVALID_MEDIA_URL", "invalid media URL", 400)
	}
	res, e := client.Do(r)
	if e != nil {
		return domains.E("MEDIA_FETCH_FAILED", "media destination unavailable or disallowed", 400)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return domains.E("MEDIA_FETCH_FAILED", "media server did not return 200", 502)
	}
	if res.ContentLength > s.opts.MaxMediaBytes {
		return domains.E("MEDIA_TOO_LARGE", "media exceeds configured limit", 413)
	}
	name := req.Name
	if name == "" {
		name = filepath.Base(u.Path)
	}
	return s.saveUpload(c, d, res.Body, name, res.Header.Get("Content-Type"))
}
func allowedMediaIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return false
		}
	}
	return true
}
func safeMediaDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ips, e := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if e != nil || len(ips) == 0 {
		return nil, fmt.Errorf("DNS lookup failed")
	}
	for _, ip := range ips {
		if !allowedMediaIP(ip) {
			return nil, fmt.Errorf("private/reserved destination is not allowed")
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

type slotReader struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (r *slotReader) Close() error { e := r.ReadCloser.Close(); r.once.Do(r.release); return e }
