package balemeow

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// These are only cookies obtained by this native client during its own login.
// Session.Data is secret material and the gateway encrypts it with the session.
// Bind each snapshot to its exact endpoint so configuration changes cannot send
// a stored session cookie to another host.
type cookieSnapshot struct {
	Origin  string         `json:"origin"`
	Cookies []*http.Cookie `json:"cookies"`
}
type sessionData struct {
	SelfAccessHash string           `json:"self_access_hash,omitempty"`
	Cookies        []cookieSnapshot `json:"cookies,omitempty"`
}

func (c *Client) cookieOrigins() []*url.URL {
	result := []*url.URL{}
	for _, endpoint := range []string{c.opts.GRPCEndpoint, c.opts.WebSocketEndpoint} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" {
			continue
		}
		if u.Scheme == "wss" {
			u.Scheme = "https"
		}
		if u.Scheme == "ws" {
			u.Scheme = "http"
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		result = append(result, u)
	}
	return result
}
func (c *Client) snapshotCookies() json.RawMessage {
	data := sessionData{}
	for _, u := range c.cookieOrigins() {
		cookies := c.opts.HTTPClient.Jar.Cookies(u)
		if len(cookies) > 0 {
			data.Cookies = append(data.Cookies, cookieSnapshot{Origin: u.String(), Cookies: cookies})
		}
	}
	if len(data.Cookies) == 0 {
		return nil
	}
	out, _ := json.Marshal(data)
	return out
}
func (c *Client) restoreCookies(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > 64<<10 {
		return boundedError("INVALID_SESSION", "session metadata exceeds limit", 400)
	}
	var data sessionData
	if json.Unmarshal(raw, &data) != nil || len(data.Cookies) > 2 {
		return boundedError("INVALID_SESSION", "invalid stored cookie metadata", 400)
	}
	allowed := map[string]*url.URL{}
	for _, u := range c.cookieOrigins() {
		allowed[u.String()] = u
	}
	for _, snapshot := range data.Cookies {
		u := allowed[snapshot.Origin]
		if u == nil || len(snapshot.Cookies) > 32 {
			return boundedError("INVALID_SESSION", "stored session belongs to another endpoint", 400)
		}
		for _, cookie := range snapshot.Cookies {
			if cookie == nil || cookie.Name == "" || strings.ContainsAny(cookie.Name+cookie.Value, "\r\n") || len(cookie.Name)+len(cookie.Value) > 16<<10 {
				return boundedError("INVALID_SESSION", "invalid stored cookie", 400)
			}
			// Snapshots are scoped to the exact previously configured host and path.
			cookie.Domain = ""
			cookie.Path = u.Path
			cookie.Secure = u.Scheme == "https"
		}
	}
	if data.SelfAccessHash != "" {
		hash, err := strconv.ParseInt(data.SelfAccessHash, 10, 64)
		if err != nil {
			return boundedError("INVALID_SESSION", "invalid account reference", 400)
		}
		c.mu.Lock()
		if c.session != nil {
			c.peerHashes["user:"+c.session.UserID] = hash
		}
		c.mu.Unlock()
	}
	for _, snapshot := range data.Cookies {
		c.opts.HTTPClient.Jar.SetCookies(allowed[snapshot.Origin], snapshot.Cookies)
	}
	return nil
}

func (c *Client) clearCookies() { c.opts.HTTPClient.Jar.(*accountCookieJar).clear() }

// A stable wrapper allows logout to erase every cookie, including domain-wide
// cookies whose original attributes net/http.Jar.Cookies intentionally hides,
// without racing concurrent HTTP requests reading Client.Jar.
type accountCookieJar struct {
	mu  sync.RWMutex
	jar *cookiejar.Jar
}

func newAccountCookieJar() *accountCookieJar {
	jar, _ := cookiejar.New(nil)
	return &accountCookieJar{jar: jar}
}
func (j *accountCookieJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.jar.Cookies(u)
}
func (j *accountCookieJar) SetCookies(u *url.URL, c []*http.Cookie) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	j.jar.SetCookies(u, c)
}
func (j *accountCookieJar) clear() { j.mu.Lock(); defer j.mu.Unlock(); j.jar, _ = cookiejar.New(nil) }
