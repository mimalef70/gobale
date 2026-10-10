package rest

import (
	"context"
	"errors"
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
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/mediafile"
)

func (s *Server) upload(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	release, e := s.acquireMedia(c.Context(), d)
	if e != nil {
		return e
	}
	defer release()
	var body io.Reader = c.Request().BodyStream()
	if body == nil {
		body = strings.NewReader(string(c.Body()))
	}
	return s.saveUpload(c, d, body, c.Get("X-Filename", "upload.bin"), c.Get("Content-Type", "application/octet-stream"))
}
func (s *Server) saveUpload(c fiber.Ctx, d domains.Device, body io.Reader, name, contentType string) error {
	manager, e := s.uploadManager()
	if e != nil {
		return e
	}
	upload, e := manager.Begin(c.Context(), d.ConnectionID)
	if e != nil {
		return domains.E("MEDIA_UPLOAD_FAILED", "media staging is unavailable", 503)
	}
	defer upload.Release()
	n, e := io.Copy(upload.File, io.LimitReader(body, s.opts.MaxMediaBytes+1))
	if e != nil {
		return domains.E("MEDIA_UPLOAD_FAILED", "incomplete upload", 400)
	}
	if n > s.opts.MaxMediaBytes {
		return domains.E("MEDIA_TOO_LARGE", "media exceeds configured limit", 413)
	}
	if e = upload.Publish(); e != nil {
		return domains.E("MEDIA_UPLOAD_FAILED", "media could not be persisted", 503)
	}
	m := domains.Media{ID: upload.ID(), ConnectionID: d.ConnectionID, Name: filepath.Base(name), ContentType: contentType, Size: n, Path: upload.RelativePath(), CreatedAt: time.Now().UTC()}
	if e = s.store.SaveMedia(c.Context(), m); e != nil {
		return e
	}
	return c.Status(201).JSON(map[string]any{"code": "SUCCESS", "message": "Media stored locally", "results": m})
}

// Embedded/test servers initialize on their first upload. The executable passes
// its already-locked manager before starting any clients or accepting requests.
func (s *Server) uploadManager() (*mediafile.Manager, error) {
	s.mediaOnce.Do(func() {
		if s.opts.MediaManager != nil {
			return
		}
		s.opts.MediaManager, s.mediaErr = mediafile.Open(s.opts.MediaRoot, s.store.MediaFileRegistered)
		if s.mediaErr != nil {
			return
		}
		s.App.Hooks().OnPostShutdown(func(error) error { return s.opts.MediaManager.Close() })
	})
	if s.mediaErr != nil {
		return nil, domains.E("MEDIA_UPLOAD_FAILED", "media root is unavailable or already owned", 503)
	}
	return s.opts.MediaManager, nil
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
	f, e := os.Open(absPath)
	if e != nil {
		return domains.E("MEDIA_NOT_FOUND", "media file unavailable", 404)
	}
	stream.ReadCloser = f
	c.Set("Content-Type", m.ContentType)
	c.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": m.Name}))
	c.Set("X-Content-Type-Options", "nosniff")
	if err := c.SendStream(stream, int(m.Size)); err != nil {
		return err
	}
	handedOff = true
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
	reader, media, err := s.service.Download(c.Context(), d.ID, peer, c.Params("message_id"))
	if err != nil {
		return err
	}
	stream.ReadCloser = reader
	if media.Size < 0 || media.Size > s.opts.MaxMediaBytes {
		return domains.E("MEDIA_TOO_LARGE", "media exceeds configured limit", 413)
	}
	c.Set("Content-Type", media.ContentType)
	c.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(media.Name)}))
	c.Set("X-Content-Type-Options", "nosniff")
	if err = c.SendStream(stream, int(media.Size)); err != nil {
		return err
	}
	handedOff = true
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
	release, e := s.acquireMedia(c.Context(), d)
	if e != nil {
		return e
	}
	defer release()
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
	err     error
}

func (r *slotReader) Close() error {
	r.once.Do(func() {
		defer r.release()
		if r.ReadCloser != nil {
			r.err = r.ReadCloser.Close()
		}
	})
	return r.err
}

// Queue only the admission phase under a bounded context. A handed-off stream
// keeps its existing lifetime and releases the permit when its reader closes.
func (s *Server) acquireMedia(ctx context.Context, d domains.Device) (func(), error) {
	wait, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	providers, err := s.service.ActiveProviders(wait)
	if err != nil {
		return nil, err
	}
	s.mediaPool.SetProviders(providers)
	release, err := s.mediaPool.AcquireFor(wait, d.Provider, d.ConnectionID)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, domains.E("REQUEST_TIMEOUT", "media capacity wait deadline exceeded", 504)
	}
	return release, err
}
