package rubikameow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func avatarPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func avatarReference(body []byte) object {
	return object{"file_id": "123", "dc_id": "4", "access_hash_rec": "private-receive", "size": json.Number(stringInt(len(body))), "url": "https://untrusted.invalid/private"}
}
func stringInt(n int) string { b, _ := json.Marshal(n); return string(b) }
func TestAvatarUsesFreshAccountReferenceAndInspectsBytes(t *testing.T) {
	body := avatarPNG(t)
	lookups, downloads := 0, 0
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		lookups++
		if method != "getAvatars" || input.str("object_guid") != "u0peer" {
			t.Error("wrong lookup")
		}
		if lookups > 1 {
			return object{"avatars": []any{}}
		}
		return object{"avatars": []any{object{"main": avatarReference(body), "thumbnail": avatarReference(body)}}}
	})
	original := c.http.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	c.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "messenger4.iranlms.ir" {
			return original.RoundTrip(r)
		}
		downloads++
		if r.URL.Scheme != "https" || r.URL.Path != "/GetFile.ashx" || r.Header.Get("access-hash-rec") != "private-receive" || r.Header.Get("auth") != fixtureAuth {
			t.Error("wrong media scope")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	stream, info, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "large")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(stream)
	_ = stream.Close()
	if !bytes.Equal(got, body) || info.ContentType != "image/png" || info.Width != 2 || info.Height != 3 {
		t.Fatalf("invalid projection: %#v", info)
	}
	_, _, err = c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "small")
	var de *domains.Error
	if !errors.As(err, &de) || de.Code != "AVATAR_NOT_FOUND" || downloads != 1 || lookups != 2 {
		t.Fatalf("stale reference reused: %v", err)
	}
}
func TestAvatarRejectsMalformedReferencesAndBytes(t *testing.T) {
	for _, kind := range []string{"dc", "large", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			body := []byte("invalid-image")
			ref := avatarReference(body)
			if kind == "dc" {
				ref["dc_id"] = "4.evil"
			}
			if kind == "large" {
				ref["size"] = json.Number("8388609")
			}
			c, _ := newRPCFixture(t, func(string, object, bool) object {
				return object{"avatars": []any{object{"main": ref, "thumbnail": ref}}}
			})
			original := c.http.Transport
			if original == nil {
				original = http.DefaultTransport
			}
			downloads := 0
			c.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "messenger4.iranlms.ir" {
					downloads++
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
				}
				return original.RoundTrip(r)
			})
			stream, _, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "large")
			if stream != nil || err == nil || (kind != "corrupt" && downloads != 0) {
				t.Fatalf("unsafe avatar accepted: %v", err)
			}
		})
	}
}
