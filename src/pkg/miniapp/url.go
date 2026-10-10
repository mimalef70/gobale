package miniapp

import (
	"encoding/json"
	"net/url"
	"strings"
)

// LaunchOptions only packages data into a browser fragment; GoOmni does not run
// an iframe bridge. Version and Platform are explicit, not claimed host support.
type LaunchOptions struct {
	Version  string
	Platform string
	Theme    map[string]string
}

// BuildLaunchURL is a pure URL helper, NOT signature validation. It requires a
// syntactically signed query, and never creates unsigned fallback authentication.
// Call Verify before trusting data received from an untrusted client.
func BuildLaunchURL(base, initData string, opts LaunchOptions) (string, error) {
	if len(base) > 8192 {
		return "", ErrMalformed
	}
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", ErrMalformed
	}
	if _, e := ParseUnverified(initData); e != nil {
		return "", e
	}
	if opts.Version == "" || len(opts.Version) > 32 || opts.Platform == "" || len(opts.Platform) > 32 || strings.ContainsAny(opts.Version+opts.Platform, "\r\n\x00") {
		return "", ErrOptions
	}
	params := url.Values{"tgWebAppData": {initData}, "tgWebAppVersion": {opts.Version}, "tgWebAppPlatform": {opts.Platform}}
	if len(opts.Theme) > 32 {
		return "", ErrOptions
	}
	if len(opts.Theme) > 0 {
		for k, v := range opts.Theme {
			if !validKey(k) || len(v) > 128 {
				return "", ErrOptions
			}
		}
		b, e := json.Marshal(opts.Theme)
		if e != nil {
			return "", ErrOptions
		}
		params.Set("tgWebAppThemeParams", string(b))
	}
	u.Fragment = params.Encode()
	return u.String(), nil
}
