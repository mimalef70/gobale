package rest

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

type adminTestResponse struct {
	Code    string `json:"code"`
	Results struct {
		CSRF     string    `json:"csrf_token"`
		Expires  time.Time `json:"expires_at"`
		Absolute time.Time `json:"absolute_expires_at"`
	} `json:"results"`
}

func adminTestApp(t *testing.T, options AdminSessionOptions) (*AdminSessions, *fiber.App) {
	t.Helper()
	sessions, err := NewAdminSessions(options)
	require.NoError(t, err)
	app := fiber.New()
	r := app.Group(options.BasePath)
	sessions.Register(r)
	r.Get("/ui/api/private", sessions.Protect, func(c fiber.Ctx) error { return success(c, nil) })
	r.Post("/ui/api/private", sessions.Protect, func(c fiber.Ctx) error { return success(c, nil) })
	r.Get("/ui/", sessions.Security, func(c fiber.Ctx) error { return c.SendString("shell") })
	return sessions, app
}
func adminTestRequest(t *testing.T, app *fiber.App, method, target, body, origin, csrf string, cookie *http.Cookie) (*http.Response, adminTestResponse) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := app.Test(req)
	require.NoError(t, err)
	data, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	res.Body.Close()
	var decoded adminTestResponse
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		require.NoError(t, json.Unmarshal(data, &decoded))
	}
	return res, decoded
}
func adminTestLogin(t *testing.T, app *fiber.App, base, origin string) (*http.Cookie, adminTestResponse) {
	t.Helper()
	res, body := adminTestRequest(t, app, "POST", origin+base+"/ui/auth/session", `{"username":"operator","password":" padded secret "}`, origin, "", nil)
	require.Equal(t, 200, res.StatusCode, body.Code)
	require.Len(t, res.Cookies(), 1)
	require.Len(t, body.Results.CSRF, 43)
	return res.Cookies()[0], body
}

func TestAdminSessionLoginProtectionLogout(t *testing.T) {
	sessions, app := adminTestApp(t, AdminSessionOptions{BasicAuth: "operator: padded secret ", BasePath: "/gateway"})
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	sessions.now = func() time.Time { return now }
	cookie, login := adminTestLogin(t, app, "/gateway", "http://localhost:3000")
	require.Equal(t, "/gateway/ui/", cookie.Path)
	require.True(t, cookie.HttpOnly)
	require.False(t, cookie.Secure)
	require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	require.Equal(t, now.Add(30*time.Minute), login.Results.Expires)
	require.Equal(t, now.Add(8*time.Hour), login.Results.Absolute)
	require.Contains(t, sessions.sessions, sha256.Sum256([]byte(cookie.Value)))

	res, read := adminTestRequest(t, app, "GET", "http://localhost:3000/gateway/ui/auth/session", "", "", "", cookie)
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, login.Results.CSRF, read.Results.CSRF)
	require.Empty(t, res.Header.Get("WWW-Authenticate"))
	require.Equal(t, "no-store", res.Header.Get("Cache-Control"))

	for _, tc := range []struct {
		origin, csrf string
		want         int
	}{
		{"", login.Results.CSRF, 403},
		{"http://other.example", login.Results.CSRF, 403},
		{"http://localhost:3000", "", 403},
		{"http://localhost:3000", "wrong", 403},
		{"http://localhost:3000", login.Results.CSRF, 200},
	} {
		res, _ := adminTestRequest(t, app, "POST", "http://localhost:3000/gateway/ui/api/private", `{}`, tc.origin, tc.csrf, cookie)
		require.Equal(t, tc.want, res.StatusCode)
	}
	res, _ = adminTestRequest(t, app, "DELETE", "http://localhost:3000/gateway/ui/auth/session", "", "http://localhost:3000", login.Results.CSRF, cookie)
	require.Equal(t, 200, res.StatusCode)
	require.Len(t, res.Cookies(), 1)
	require.Equal(t, "/gateway/ui/", res.Cookies()[0].Path)
	require.Equal(t, -1, res.Cookies()[0].MaxAge)
	require.Empty(t, sessions.sessions)
	res, _ = adminTestRequest(t, app, "GET", "http://localhost:3000/gateway/ui/api/private", "", "", "", cookie)
	require.Equal(t, 401, res.StatusCode)
	require.Empty(t, res.Header.Get("WWW-Authenticate"))
}

func TestAdminSessionsRejectBasicAndBadOrigins(t *testing.T) {
	_, app := adminTestApp(t, AdminSessionOptions{BasicAuth: "operator: padded secret "})
	for _, tc := range []struct {
		host, origin string
		status       int
	}{
		{"http://localhost:3000", "", 403},
		{"http://localhost:3000", "null", 403},
		{"http://localhost:3000", "http://evil.example", 403},
		{"http://evil.example", "http://evil.example", 403},
		{"http://192.168.1.10:3000", "http://192.168.1.10:3000", 403},
		{"http://localhost.attacker.example", "http://localhost.attacker.example", 403},
		{"http://127.0.0.1:3000", "http://127.0.0.1:3000", 200},
		{"http://[::1]:3000", "http://[::1]:3000", 200},
	} {
		res, _ := adminTestRequest(t, app, "POST", tc.host+"/ui/auth/session", `{"username":"operator","password":" padded secret "}`, tc.origin, "", nil)
		require.Equal(t, tc.status, res.StatusCode, tc.host+" "+tc.origin)
	}
	req := httptest.NewRequest("GET", "http://localhost:3000/ui/api/private", nil)
	req.SetBasicAuth("operator", " padded secret ")
	res, err := app.Test(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 401, res.StatusCode)
	require.Empty(t, res.Header.Get("WWW-Authenticate"))
	for _, target := range []string{"http://evil.example/ui/", "http://localhost:3000/ui/"} {
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("X-Forwarded-Host", "localhost:3000")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Origin", "https://localhost:3000")
		res, err := app.Test(req)
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, 403, res.StatusCode)
	}
}

func TestAdminSessionPublicOriginSecureCookie(t *testing.T) {
	_, app := adminTestApp(t, AdminSessionOptions{BasicAuth: "operator: padded secret ", BasePath: "/proxy", PublicOrigin: "https://admin.example"})
	cookie, login := adminTestLogin(t, app, "/proxy", "https://admin.example")
	require.True(t, cookie.Secure)
	res, _ := adminTestRequest(t, app, "POST", "http://admin.example/proxy/ui/api/private", `{}`, "https://admin.example", login.Results.CSRF, cookie)
	require.Equal(t, 200, res.StatusCode, "explicit public origin supports TLS terminating proxy without forwarded headers")
	for _, origin := range []string{"http://admin.example", "https://evil.example", "https://admin.example:443"} {
		res, _ := adminTestRequest(t, app, "POST", "http://admin.example/proxy/ui/api/private", `{}`, origin, login.Results.CSRF, cookie)
		require.Equal(t, 403, res.StatusCode)
	}
	res, _ = adminTestRequest(t, app, "GET", "http://localhost:3000/proxy/ui/api/private", "", "", "", cookie)
	require.Equal(t, 403, res.StatusCode)
	for _, bad := range []string{"http://admin.example", "https://admin.example/path", "https://user:pass@admin.example", "https://admin.example?x=1", "https://admin.example#fragment", "https://admin.example:abc", "https://admin.example%2e"} {
		_, err := NewAdminSessions(AdminSessionOptions{BasicAuth: "operator:secret", PublicOrigin: bad})
		require.Error(t, err, bad)
	}
}

func TestAdminSessionExpiryRotationAndLimits(t *testing.T) {
	sessions, app := adminTestApp(t, AdminSessionOptions{BasicAuth: "operator: padded secret "})
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	sessions.now = func() time.Time { return now }
	cookie, login := adminTestLogin(t, app, "", "http://localhost:3000")
	for i := 0; i < 23; i++ {
		now = now.Add(20 * time.Minute)
		res, body := adminTestRequest(t, app, "GET", "http://localhost:3000/ui/auth/session", "", "", "", cookie)
		require.Equal(t, 200, res.StatusCode)
		require.False(t, body.Results.Expires.After(login.Results.Absolute))
	}
	now = login.Results.Absolute
	res, _ := adminTestRequest(t, app, "GET", "http://localhost:3000/ui/auth/session", "", "", "", cookie)
	require.Equal(t, 401, res.StatusCode)
	require.Empty(t, sessions.sessions)
	cookie, _ = adminTestLogin(t, app, "", "http://localhost:3000")
	now = now.Add(adminSessionIdle)
	res, _ = adminTestRequest(t, app, "GET", "http://localhost:3000/ui/auth/session", "", "", "", cookie)
	require.Equal(t, 401, res.StatusCode)

	cookie, _ = adminTestLogin(t, app, "", "http://localhost:3000")
	res, _ = adminTestRequest(t, app, "POST", "http://localhost:3000/ui/auth/session", `{"username":"operator","password":" padded secret "}`, "http://localhost:3000", "", cookie)
	require.Equal(t, 200, res.StatusCode)
	require.NotEqual(t, cookie.Value, res.Cookies()[0].Value)
	require.Len(t, sessions.sessions, 1)
	res, _ = adminTestRequest(t, app, "GET", "http://localhost:3000/ui/auth/session", "", "", "", cookie)
	require.Equal(t, 401, res.StatusCode)

	for len(sessions.sessions) < adminSessionLimit {
		token, err := adminRandomToken()
		require.NoError(t, err)
		sessions.sessions[sha256.Sum256([]byte(token))] = &adminSession{idleUntil: now.Add(time.Minute), absoluteUntil: now.Add(time.Hour)}
	}
	res, body := adminTestRequest(t, app, "POST", "http://localhost:3000/ui/auth/session", `{"username":"operator","password":" padded secret "}`, "http://localhost:3000", "", nil)
	require.Equal(t, 503, res.StatusCode)
	require.Equal(t, "UI_SESSION_LIMIT", body.Code)
	require.Len(t, sessions.sessions, adminSessionLimit)
}

func TestAdminLoginRateAndCredentialValidation(t *testing.T) {
	sessions, app := adminTestApp(t, AdminSessionOptions{BasicAuth: "operator: padded secret "})
	now := time.Now()
	sessions.now = func() time.Time { return now }
	for i := 0; i < adminLoginPerIP; i++ {
		res, _ := adminTestRequest(t, app, "POST", "http://localhost:3000/ui/auth/session", `{"username":"operator","password":"padded secret"}`, "http://localhost:3000", "", nil)
		require.Equal(t, 401, res.StatusCode, "password whitespace is meaningful")
	}
	res, _ := adminTestRequest(t, app, "POST", "http://localhost:3000/ui/auth/session", `{"username":"operator","password":" padded secret "}`, "http://localhost:3000", "", nil)
	require.Equal(t, 429, res.StatusCode)
	require.Equal(t, "60", res.Header.Get("Retry-After"))
	now = now.Add(time.Minute)
	adminTestLogin(t, app, "", "http://localhost:3000")
	for _, body := range []string{`{"username":"operator","username":"other","password":"x"}`, `{"username":"operator","password":"x","extra":true}`, `[]`, strings.Repeat("x", 8193)} {
		res, _ := adminTestRequest(t, app, "POST", "http://localhost:3000/ui/auth/session", body, "http://localhost:3000", "", nil)
		require.Equal(t, 400, res.StatusCode)
	}
	sessions.loginIPs = make(map[string]adminLoginBucket)
	for i := 0; i < adminLoginIPLimit; i++ {
		sessions.loginIPs[string(rune(i))] = adminLoginBucket{since: now, count: 1}
	}
	require.False(t, sessions.allowLogin("not-known"))
	require.Len(t, sessions.loginIPs, adminLoginIPLimit)
	now = now.Add(time.Minute)
	require.True(t, sessions.allowLogin("not-known"))
	require.Len(t, sessions.loginIPs, 1)
}

func TestAdminLoginGlobalRateBound(t *testing.T) {
	sessions, err := NewAdminSessions(AdminSessionOptions{BasicAuth: "operator:secret"})
	require.NoError(t, err)
	now := time.Now()
	sessions.now = func() time.Time { return now }
	for i := 0; i < adminLoginGlobal; i++ {
		require.True(t, sessions.allowLogin(string(rune(i))))
	}
	require.False(t, sessions.allowLogin("over-global-limit"))
	require.Len(t, sessions.loginIPs, adminLoginGlobal)
	now = now.Add(adminLoginWindow)
	require.True(t, sessions.allowLogin("over-global-limit"))
	require.Len(t, sessions.loginIPs, 1)
}

func TestAdminLoginUsernameCannotRepartitionPassword(t *testing.T) {
	_, app := adminTestApp(t, AdminSessionOptions{BasicAuth: "operator:part:secret"})
	res, _ := adminTestRequest(t, app, "POST", "http://localhost:3000/ui/auth/session", `{"username":"operator:part","password":"secret"}`, "http://localhost:3000", "", nil)
	require.Equal(t, 400, res.StatusCode)
	res, _ = adminTestRequest(t, app, "POST", "http://localhost:3000/ui/auth/session", `{"username":"operator","password":"part:secret"}`, "http://localhost:3000", "", nil)
	require.Equal(t, 200, res.StatusCode)
}

func TestAdminBrowserNamespaceNeverFallsThroughToBasic(t *testing.T) {
	srv, _, _ := integrationBrowserServer(t, "/gateway", true, nil)
	for _, tc := range []struct{ method, path string }{
		{"PUT", "/ui/auth/session"},
		{"POST", "/ui/auth/unknown"},
		{"POST", "/ui"},
		{"PATCH", "/ui/unknown"},
	} {
		res, body := browserIntegrationRequest(t, srv, browserIntegrationSession{}, tc.method, tc.path, nil, nil, false)
		require.Equal(t, 404, res.StatusCode, tc.path)
		require.Equal(t, "NOT_FOUND", body["code"])
		require.Empty(t, res.Header.Get("WWW-Authenticate"))
	}
	res, body := browserIntegrationRequest(t, srv, browserIntegrationSession{}, "GET", "/devices", nil, nil, false)
	require.Equal(t, 401, res.StatusCode)
	require.Equal(t, "UNAUTHORIZED", body["code"])
	require.Contains(t, res.Header.Get("WWW-Authenticate"), "Basic")
}
