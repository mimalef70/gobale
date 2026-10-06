package rest

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/pkg/utils"
)

const (
	adminSessionCookie   = "gobale_admin"
	adminSessionIdle     = 30 * time.Minute
	adminSessionAbsolute = 8 * time.Hour
	adminSessionLimit    = 100
	adminLoginWindow     = time.Minute
	adminLoginPerIP      = 5
	adminLoginGlobal     = 100
	adminLoginIPLimit    = 1024
)

// AdminSessionOptions describes the same-origin administrative browser surface.
// PublicOrigin is an explicit HTTPS origin for a reverse proxy; forwarded
// headers are never used to infer it. Without it only literal loopback hosts work.
type AdminSessionOptions struct {
	BasicAuth    string
	BasePath     string
	PublicOrigin string
}

type adminSession struct {
	csrf          string
	csrfHash      [32]byte
	idleUntil     time.Time
	absoluteUntil time.Time
}
type adminLoginBucket struct {
	since time.Time
	count int
}

// AdminSessions holds bounded, process-local sessions. Restart intentionally
// signs all browsers out. The bearer cookie itself is never stored in memory.
type AdminSessions struct {
	mu             sync.Mutex
	credentialHash [32]byte
	publicOrigin   string
	publicHost     string
	cookiePath     string
	sessions       map[[32]byte]*adminSession
	loginIPs       map[string]adminLoginBucket
	loginGlobal    adminLoginBucket
	now            func() time.Time
}

func NewAdminSessions(opts AdminSessionOptions) (*AdminSessions, error) {
	username, password, ok := strings.Cut(opts.BasicAuth, ":")
	if !ok || username == "" || password == "" {
		return nil, fmt.Errorf("administrative credentials are required")
	}
	base := strings.TrimRight(opts.BasePath, "/")
	if base != "" && (!strings.HasPrefix(base, "/") || strings.ContainsAny(base, "*?:#\\\x00\r\n") || strings.Contains(base, "..")) {
		return nil, fmt.Errorf("invalid administrative base path")
	}
	s := &AdminSessions{credentialHash: sha256.Sum256([]byte(opts.BasicAuth)), cookiePath: base + "/ui/", sessions: make(map[[32]byte]*adminSession), loginIPs: make(map[string]adminLoginBucket), now: time.Now}
	if opts.PublicOrigin != "" {
		u, err := url.Parse(opts.PublicOrigin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return nil, fmt.Errorf("UI public origin must be an HTTPS origin without path, credentials, query or fragment")
		}
		if _, err = originHost(u.Host); err != nil {
			return nil, fmt.Errorf("invalid UI public origin host")
		}
		s.publicHost = strings.ToLower(u.Host)
		s.publicOrigin = "https://" + s.publicHost
	}
	return s, nil
}

// Register receives the router already prefixed with APP_BASE_PATH.
func (s *AdminSessions) Register(r fiber.Router) {
	r.Post("/ui/auth/session", s.login)
	r.Get("/ui/auth/session", s.current)
	r.Delete("/ui/auth/session", s.logout)
}

// Security applies to the static UI as well as authentication and UI API routes.
func (s *AdminSessions) Security(c fiber.Ctx) error {
	if !s.guard(c, false) {
		return nil
	}
	return c.Next()
}

func (s *AdminSessions) guard(c fiber.Ctx, requireOrigin bool) bool {
	c.Set("Cache-Control", "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Referrer-Policy", "no-referrer")
	c.Set("X-Frame-Options", "DENY")
	c.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	c.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; object-src 'none'; frame-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	c.Response().Header.Del("WWW-Authenticate")
	origin, ok := s.expectedOrigin(c)
	if !ok {
		_ = adminError(c, 403, "UI_ORIGIN_REJECTED", "administrative UI host is not allowed")
		return false
	}
	got := c.Get("Origin")
	if (requireOrigin && got == "") || (got != "" && got != origin) {
		_ = adminError(c, 403, "UI_ORIGIN_REJECTED", "same-origin request required")
		return false
	}
	if site := c.Get("Sec-Fetch-Site"); site == "cross-site" {
		_ = adminError(c, 403, "UI_ORIGIN_REJECTED", "same-origin request required")
		return false
	}
	return true
}

func originHost(authority string) (string, error) {
	if authority == "" || strings.ContainsAny(authority, "/\\@%?#\x00\t\r\n ") {
		return "", fmt.Errorf("invalid authority")
	}
	u, err := url.Parse("http://" + authority)
	if err != nil || u.Host != authority || u.Hostname() == "" {
		return "", fmt.Errorf("invalid authority")
	}
	if strings.Contains(authority, ":") && !strings.HasSuffix(authority, "]") && u.Port() == "" {
		return "", fmt.Errorf("invalid port")
	}
	return strings.ToLower(u.Hostname()), nil
}

func (s *AdminSessions) expectedOrigin(c fiber.Ctx) (string, bool) {
	// Read the actual Host header, not Fiber's forwarded host/protocol helpers.
	host := strings.ToLower(string(c.Request().Header.Host()))
	name, err := originHost(host)
	if err != nil {
		return "", false
	}
	if s.publicOrigin != "" {
		return s.publicOrigin, host == s.publicHost
	}
	ip := net.ParseIP(name)
	if name != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", false
	}
	scheme := "http"
	if c.RequestCtx().TLSConnectionState() != nil {
		scheme = "https"
	}
	return scheme + "://" + host, true
}

func adminMutation(method string) bool {
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}

// Protect authenticates only browser cookies; Basic credentials never grant
// access here. Root mounts it only on the finite UI API allowlist.
func (s *AdminSessions) Protect(c fiber.Ctx) error {
	if !s.guard(c, adminMutation(c.Method())) {
		return nil
	}
	if _, ok := s.authorize(c, adminMutation(c.Method())); !ok {
		return nil
	}
	return c.Next()
}

func (s *AdminSessions) authorize(c fiber.Ctx, csrfRequired bool) (*adminSession, bool) {
	token := c.Cookies(adminSessionCookie)
	if len(token) != 43 {
		_ = adminError(c, 401, "UI_UNAUTHORIZED", "sign in to the administrative UI")
		return nil, false
	}
	key := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[key]
	if !ok || !now.Before(session.idleUntil) || !now.Before(session.absoluteUntil) {
		delete(s.sessions, key)
		s.clearCookie(c)
		_ = adminError(c, 401, "UI_UNAUTHORIZED", "administrative session expired")
		return nil, false
	}
	if csrfRequired {
		got := sha256.Sum256([]byte(c.Get("X-CSRF-Token")))
		if subtle.ConstantTimeCompare(got[:], session.csrfHash[:]) != 1 {
			_ = adminError(c, 403, "UI_CSRF_REJECTED", "valid CSRF token required")
			return nil, false
		}
	}
	session.idleUntil = minTime(now.Add(adminSessionIdle), session.absoluteUntil)
	s.setCookie(c, token, session.idleUntil)
	copySession := *session
	return &copySession, true
}

func (s *AdminSessions) login(c fiber.Ctx) error {
	if !s.guard(c, true) {
		return nil
	}
	if !s.allowLogin(c.RequestCtx().RemoteIP().String()) {
		c.Set("Retry-After", "60")
		return adminError(c, 429, "UI_LOGIN_RATE_LIMITED", "too many sign-in attempts; try again later")
	}
	mediaType, _, err := mime.ParseMediaType(c.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return adminError(c, 415, "INVALID_REQUEST", "JSON credentials required")
	}
	var reader io.Reader = c.Request().BodyStream()
	if reader == nil {
		reader = bytes.NewReader(c.Body())
	}
	body, err := io.ReadAll(io.LimitReader(reader, 8193))
	if err != nil || len(body) > 8192 || domains.ValidateJSONObject(body) != nil {
		return adminError(c, 400, "INVALID_REQUEST", "invalid credential request")
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&req); err != nil || req.Username == "" || strings.Contains(req.Username, ":") || req.Password == "" {
		return adminError(c, 400, "INVALID_REQUEST", "username and password are required")
	}
	got := sha256.Sum256([]byte(req.Username + ":" + req.Password))
	if subtle.ConstantTimeCompare(got[:], s.credentialHash[:]) != 1 {
		return adminError(c, 401, "UI_UNAUTHORIZED", "invalid administrative credentials")
	}
	token, err := adminRandomToken()
	if err != nil {
		return adminError(c, 500, "UI_SESSION_FAILED", "could not create administrative session")
	}
	csrf, err := adminRandomToken()
	if err != nil {
		return adminError(c, 500, "UI_SESSION_FAILED", "could not create administrative session")
	}
	now := s.now().UTC()
	session := &adminSession{csrf: csrf, csrfHash: sha256.Sum256([]byte(csrf)), idleUntil: now.Add(adminSessionIdle), absoluteUntil: now.Add(adminSessionAbsolute)}
	s.mu.Lock()
	for key, value := range s.sessions {
		if !now.Before(value.idleUntil) || !now.Before(value.absoluteUntil) {
			delete(s.sessions, key)
		}
	}
	// A fresh sign-in rotates and revokes an existing browser session.
	if old := c.Cookies(adminSessionCookie); old != "" {
		delete(s.sessions, sha256.Sum256([]byte(old)))
	}
	if len(s.sessions) >= adminSessionLimit {
		s.mu.Unlock()
		return adminError(c, 503, "UI_SESSION_LIMIT", "administrative session capacity reached")
	}
	s.sessions[sha256.Sum256([]byte(token))] = session
	s.mu.Unlock()
	s.setCookie(c, token, session.idleUntil)
	return s.respond(c, session)
}

func (s *AdminSessions) current(c fiber.Ctx) error {
	if !s.guard(c, false) {
		return nil
	}
	session, ok := s.authorize(c, false)
	if !ok {
		return nil
	}
	return s.respond(c, session)
}
func (s *AdminSessions) logout(c fiber.Ctx) error {
	if !s.guard(c, true) {
		return nil
	}
	if _, ok := s.authorize(c, true); !ok {
		return nil
	}
	s.mu.Lock()
	delete(s.sessions, sha256.Sum256([]byte(c.Cookies(adminSessionCookie))))
	s.mu.Unlock()
	s.clearCookie(c)
	return success(c, nil)
}
func (s *AdminSessions) respond(c fiber.Ctx, session *adminSession) error {
	return success(c, map[string]any{"csrf_token": session.csrf, "expires_at": session.idleUntil, "absolute_expires_at": session.absoluteUntil})
}
func (s *AdminSessions) setCookie(c fiber.Ctx, token string, until time.Time) {
	c.Cookie(&fiber.Cookie{Name: adminSessionCookie, Value: token, Path: s.cookiePath, HTTPOnly: true, Secure: s.publicOrigin != "" || c.RequestCtx().TLSConnectionState() != nil, SameSite: "Strict", Expires: until})
}
func (s *AdminSessions) clearCookie(c fiber.Ctx) {
	c.Cookie(&fiber.Cookie{Name: adminSessionCookie, Value: "", Path: s.cookiePath, HTTPOnly: true, Secure: s.publicOrigin != "" || c.RequestCtx().TLSConnectionState() != nil, SameSite: "Strict", Expires: time.Unix(1, 0), MaxAge: -1})
}
func (s *AdminSessions) allowLogin(ip string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, bucket := range s.loginIPs {
		if !now.Before(bucket.since.Add(adminLoginWindow)) {
			delete(s.loginIPs, key)
		}
	}
	if !now.Before(s.loginGlobal.since.Add(adminLoginWindow)) {
		s.loginGlobal = adminLoginBucket{since: now}
	}
	if s.loginGlobal.count >= adminLoginGlobal {
		return false
	}
	bucket, exists := s.loginIPs[ip]
	if !exists {
		if len(s.loginIPs) >= adminLoginIPLimit {
			return false
		}
		bucket = adminLoginBucket{since: now}
	}
	if bucket.count >= adminLoginPerIP {
		return false
	}
	bucket.count++
	s.loginIPs[ip] = bucket
	s.loginGlobal.count++
	return true
}
func adminRandomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func adminError(c fiber.Ctx, status int, code, message string) error {
	return c.Status(status).JSON(utils.ResponseData{Code: code, Message: message})
}
