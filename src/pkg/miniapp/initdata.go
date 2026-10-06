// Package miniapp implements the WebAppData HMAC format documented by Telegram
// for compatible Mini Apps. Live Bale compatibility must be tested independently.
// It does not create a Bale login, obtain a bot token or validate a user session.
package miniapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrMalformed = errors.New("miniapp: malformed init data")
	ErrSignature = errors.New("miniapp: signature mismatch")
	ErrExpired   = errors.New("miniapp: authentication data expired")
	ErrFuture    = errors.New("miniapp: authentication date is in the future")
	ErrOptions   = errors.New("miniapp: invalid validation options")
)

const maxQueryBytes = 32 << 10

// Options always enforces expiry. Zero MaxAge uses five minutes; negative or
// values over seven days are rejected. Zero FutureSkew permits no future date.
type Options struct {
	Now        time.Time
	MaxAge     time.Duration
	FutureSkew time.Duration
}

// Unverified is explicitly untrusted. ParseUnverified is decoding, not login.
// Values remain strings so a consumer does not round large JSON user IDs.
type Unverified struct {
	Fields   map[string]string
	AuthDate time.Time
}

// Verified is returned only after signature and expiry checks. The token holder
// can sign arbitrary user fields: this proves that token's signature, not an
// independently observed Bale user session or the truth of caller-signed data.
type Verified struct {
	Fields    map[string]string
	AuthDate  time.Time
	ExpiresAt time.Time
}

func ParseUnverified(raw string) (Unverified, error) {
	fields, err := parse(raw, true)
	if err != nil {
		return Unverified{}, err
	}
	stamp, err := timestamp(fields["auth_date"])
	if err != nil {
		return Unverified{}, err
	}
	return Unverified{Fields: fields, AuthDate: stamp}, nil
}

// Sign signs caller-supplied fields with a caller-owned bot token. auth_date must
// be supplied explicitly. It never invents identity, dates, query IDs or tokens,
// never falls back to unsigned data, and rejects a preexisting hash.
func Sign(fields map[string]string, botToken string) (string, error) {
	if !validToken(botToken) {
		return "", ErrOptions
	}
	if _, ok := fields["hash"]; ok {
		return "", ErrMalformed
	}
	values := url.Values{}
	for k, v := range fields {
		values.Set(k, v)
	}
	raw := values.Encode()
	clean, err := parse(raw, false)
	if err != nil {
		return "", err
	}
	if _, err = timestamp(clean["auth_date"]); err != nil {
		return "", err
	}
	values.Set("hash", hex.EncodeToString(signature(clean, botToken)))
	result := values.Encode()
	if len(result) > maxQueryBytes {
		return "", ErrMalformed
	}
	return result, nil
}

func Verify(raw, botToken string, opts Options) (Verified, error) {
	if !validToken(botToken) || opts.MaxAge < 0 || opts.MaxAge > 7*24*time.Hour || opts.FutureSkew < 0 || opts.FutureSkew > 5*time.Minute {
		return Verified{}, ErrOptions
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = 5 * time.Minute
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	parsed, err := ParseUnverified(raw)
	if err != nil {
		return Verified{}, err
	}
	received, _ := hex.DecodeString(parsed.Fields["hash"])
	if !hmac.Equal(received, signature(parsed.Fields, botToken)) {
		return Verified{}, ErrSignature
	}
	if parsed.AuthDate.After(opts.Now.Add(opts.FutureSkew)) {
		return Verified{}, ErrFuture
	}
	expiry := parsed.AuthDate.Add(opts.MaxAge)
	if val, ok := parsed.Fields["expires_at"]; ok {
		explicit, e := timestamp(val)
		if e != nil || !explicit.After(parsed.AuthDate) {
			return Verified{}, ErrMalformed
		}
		if explicit.Before(expiry) {
			expiry = explicit
		}
	}
	if !opts.Now.Before(expiry) {
		return Verified{}, ErrExpired
	}
	return Verified{Fields: parsed.Fields, AuthDate: parsed.AuthDate, ExpiresAt: expiry}, nil
}

func parse(raw string, requireHash bool) (map[string]string, error) {
	if raw == "" || len(raw) > maxQueryBytes || !utf8.ValidString(raw) {
		return nil, ErrMalformed
	}
	parsed, err := url.ParseQuery(raw)
	if err != nil || len(parsed) > 64 {
		return nil, ErrMalformed
	}
	fields := make(map[string]string, len(parsed))
	for key, values := range parsed {
		if len(values) != 1 || !validKey(key) || !utf8.ValidString(values[0]) || strings.ContainsAny(values[0], "\r\n\x00") {
			return nil, ErrMalformed
		}
		fields[key] = values[0]
	}
	if _, ok := fields["auth_date"]; !ok {
		return nil, ErrMalformed
	}
	hash, present := fields["hash"]
	if requireHash && !present {
		return nil, ErrMalformed
	}
	if present {
		if len(hash) != 64 {
			return nil, ErrMalformed
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, ErrMalformed
		}
	}
	return fields, nil
}
func validKey(v string) bool {
	if len(v) == 0 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
func validToken(v string) bool {
	return v != "" && len(v) <= 4096 && utf8.ValidString(v) && !strings.ContainsAny(v, "\r\n\x00")
}
func timestamp(v string) (time.Time, error) {
	if v == "" || len(v) > 12 {
		return time.Time{}, ErrMalformed
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return time.Time{}, ErrMalformed
		}
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n <= 0 || n > 253402300799 {
		return time.Time{}, ErrMalformed
	}
	return time.Unix(n, 0).UTC(), nil
}
func signature(fields map[string]string, botToken string) []byte {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + fields[k]
	}
	key := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = key.Write([]byte(botToken))
	mac := hmac.New(sha256.New, key.Sum(nil))
	_, _ = mac.Write([]byte(strings.Join(pairs, "\n")))
	return mac.Sum(nil)
}
