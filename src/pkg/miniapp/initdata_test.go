package miniapp

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testToken = "123:fake-test-token"

func fields() map[string]string {
	return map[string]string{"auth_date": "1700000000", "query_id": "test+1", "user": `{"id":9007199254740993,"first_name":"آزمایش"}`}
}
func signed(t *testing.T) string {
	t.Helper()
	v, e := Sign(fields(), testToken)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestIndependentHMACVectorAndLosslessFields(t *testing.T) {
	raw := signed(t)
	v, _ := url.ParseQuery(raw)
	// Independently calculated with Python standard-library hmac/hashlib.
	if v.Get("hash") != "c6fe37a96f325a489338ef918a68e883617f2501893dc62402d0a81932d78b77" {
		t.Fatal("HMAC format mismatch")
	}
	result, e := Verify(raw, testToken, Options{Now: time.Unix(1700000001, 0)})
	if e != nil || result.Fields["user"] != fields()["user"] || result.Fields["query_id"] != "test+1" {
		t.Fatalf("lost signed data: %v", e)
	}
	if result.ExpiresAt.Unix() != 1700000300 {
		t.Fatal("default expiry not enforced")
	}
	if _, e := Verify(raw, "wrong", Options{Now: time.Unix(1700000001, 0)}); !errors.Is(e, ErrSignature) {
		t.Fatal(e)
	}
	v.Set("query_id", "tampered")
	if _, e := Verify(v.Encode(), testToken, Options{Now: time.Unix(1700000001, 0)}); !errors.Is(e, ErrSignature) {
		t.Fatal(e)
	}
}
func TestRejectAmbiguousOrMissingData(t *testing.T) {
	raw := signed(t)
	for _, bad := range []string{raw + "&auth_date=1700000000", raw + "&%61uth_date=1700000000", raw + "&hash=" + strings.Repeat("0", 64), raw + "&x=%ZZ", raw + "&x=a;b", raw + "&x=%00", raw + "&x=first%0Auser%3Dnew", strings.Repeat("a", maxQueryBytes+1), "auth_date=1&hash=abc", "auth_date=1&hash=" + strings.Repeat("g", 64), "hash=" + strings.Repeat("0", 64)} {
		if _, e := ParseUnverified(bad); !errors.Is(e, ErrMalformed) {
			t.Fatalf("accepted malformed query: %v", e)
		}
	}
	for _, stamp := range []string{"", "+1700000000", "1700000000x", "NaN", "0", "-1", "253402300800"} {
		p := fields()
		p["auth_date"] = stamp
		if _, e := Sign(p, testToken); !errors.Is(e, ErrMalformed) {
			t.Fatalf("accepted invalid date: %q %v", stamp, e)
		}
	}
	p := fields()
	p["hash"] = "old"
	if _, e := Sign(p, testToken); !errors.Is(e, ErrMalformed) {
		t.Fatal("silently overwrote hash")
	}
	if _, e := Sign(fields(), ""); !errors.Is(e, ErrOptions) {
		t.Fatal("unsigned fallback")
	}
}

func TestSignFieldBudgetIncludesHash(t *testing.T) {
	for _, count := range []int{63, 64, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			input := map[string]string{"auth_date": "1700000000"}
			for i := 1; i < count; i++ {
				input[fmt.Sprintf("field_%02d", i)] = "synthetic"
			}
			raw, err := Sign(input, testToken)
			if count >= 64 {
				if !errors.Is(err, ErrMalformed) || raw != "" {
					t.Fatalf("accepted %d unsigned fields, leaving no hash slot: %v", count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			verified, err := Verify(raw, testToken, Options{Now: time.Unix(1700000001, 0)})
			if err != nil || len(verified.Fields) != 64 {
				t.Fatalf("valid boundary did not round-trip: %v", err)
			}
			// Both reader entry points must enforce the same final field bound.
			if _, err := ParseUnverified(raw + "&extra=value"); !errors.Is(err, ErrMalformed) {
				t.Fatalf("parser accepted 65 signed fields: %v", err)
			}
			if _, err := Verify(raw+"&extra=value", testToken, Options{Now: time.Unix(1700000001, 0)}); !errors.Is(err, ErrMalformed) {
				t.Fatalf("verifier accepted 65 signed fields: %v", err)
			}
		})
	}
}

func TestMiniAppRejectsUnicodeControls(t *testing.T) {
	// C0 and C1 are Unicode control characters. These include TAB, ESC and DEL,
	// not just the newline and NUL cases relevant to HMAC line construction.
	controls := []rune{}
	for c := rune(0); c <= 0x1f; c++ {
		controls = append(controls, c)
	}
	for c := rune(0x7f); c <= 0x9f; c++ {
		controls = append(controls, c)
	}
	for _, control := range controls {
		t.Run(fmt.Sprintf("U%04X", control), func(t *testing.T) {
			value := "before" + string(control) + "after"
			input := fields()
			input["query_id"] = value
			if _, err := Sign(input, testToken); !errors.Is(err, ErrMalformed) {
				t.Fatalf("signing accepted a control: %v", err)
			}
			query := url.Values{"auth_date": {"1700000000"}, "query_id": {value}, "hash": {strings.Repeat("0", 64)}}
			// It must fail structural validation before signature comparison.
			if _, err := ParseUnverified(query.Encode()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("parsing accepted an encoded control: %v", err)
			}
			if _, err := Verify(query.Encode(), testToken, Options{Now: time.Unix(1700000001, 0)}); !errors.Is(err, ErrMalformed) {
				t.Fatalf("verification accepted an encoded control: %v", err)
			}
			if _, err := Sign(fields(), "token"+string(control)); !errors.Is(err, ErrOptions) {
				t.Fatalf("accepted a control in token: %v", err)
			}
		})
	}
}

func TestMiniAppPreservesNonControlUnicodeAndSpaces(t *testing.T) {
	input := fields()
	input["query_id"] = "  فارسی\u200cعربی + café 😀 = value  "
	raw, err := Sign(input, testToken)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(raw, testToken, Options{Now: time.Unix(1700000001, 0)})
	if err != nil || verified.Fields["query_id"] != input["query_id"] {
		t.Fatalf("legitimate raw field was normalized: %v", err)
	}
}
func TestMandatoryExpiryFutureBoundsAndExplicitExpiry(t *testing.T) {
	raw := signed(t)
	for _, tc := range []struct {
		opt  Options
		want error
	}{
		{Options{Now: time.Unix(1700000300, 0)}, ErrExpired},
		{Options{Now: time.Unix(1699999999, 0)}, ErrFuture},
		{Options{Now: time.Unix(1700000001, 0), MaxAge: -1}, ErrOptions},
		{Options{Now: time.Unix(1700000001, 0), MaxAge: 8 * 24 * time.Hour}, ErrOptions},
		{Options{Now: time.Unix(1700000001, 0), FutureSkew: -1}, ErrOptions},
		{Options{Now: time.Unix(1700000001, 0), FutureSkew: 6 * time.Minute}, ErrOptions},
	} {
		if _, e := Verify(raw, testToken, tc.opt); !errors.Is(e, tc.want) {
			t.Fatalf("got %v want %v", e, tc.want)
		}
	}
	if _, e := Verify(raw, testToken, Options{Now: time.Unix(1699999999, 0), FutureSkew: time.Second}); e != nil {
		t.Fatal(e)
	}
	p := fields()
	p["expires_at"] = "1700000010"
	raw, e := Sign(p, testToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Verify(raw, testToken, Options{Now: time.Unix(1700000010, 0)}); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	p["expires_at"] = "1699999999"
	raw, _ = Sign(p, testToken)
	if _, e := Verify(raw, testToken, Options{Now: time.Unix(1700000001, 0)}); !errors.Is(e, ErrMalformed) {
		t.Fatal(e)
	}
}
func TestLaunchURLDoesNotInventCredentials(t *testing.T) {
	opts := LaunchOptions{Version: "7.0", Platform: "weba", Theme: map[string]string{"bg_color": "#ffffff"}}
	out, e := BuildLaunchURL("https://example.test/app?keep=1", signed(t), opts)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(out)
	p, _ := url.ParseQuery(u.Fragment)
	if u.Query().Get("keep") != "1" || p.Get("tgWebAppData") != signed(t) {
		t.Fatal("URL damaged data")
	}
	for _, base := range []string{"http://example.test", "javascript:alert(1)", "https://user:pass@example.test", "https://example.test/#old"} {
		if _, e := BuildLaunchURL(base, signed(t), opts); e == nil {
			t.Fatal("accepted unsafe URL")
		}
	}
	if _, e := BuildLaunchURL("https://example.test/", "auth_date=1700000000", opts); e == nil {
		t.Fatal("unsigned fallback")
	}
	if _, e := BuildLaunchURL("https://example.test/", signed(t), LaunchOptions{}); e == nil {
		t.Fatal("invented host version")
	}
}
func FuzzParseUnverified(f *testing.F) {
	f.Add("auth_date=1&hash=" + strings.Repeat("0", 64))
	f.Add("auth_date=1&auth_date=2")
	f.Add("auth_date=1&query_id=%09%1B%C2%85&hash=" + strings.Repeat("0", 64))
	f.Fuzz(func(t *testing.T, raw string) { _, _ = ParseUnverified(raw) })
}
