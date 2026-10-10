package eitaameow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestAssetReadsUseFiniteWireAndHideReferences(t *testing.T) {
	tests := []struct{ name, method, body, constructor string }{
		{"account.avatars", "photos.getUserPhotos", `{"limit":1,"offset":0}`, "photos.photosSlice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				if method != tt.method {
					t.Error(method)
				}
				if tt.name == "account.avatars" {
					if asObject(p["user_id"]).str("_") != "inputUserSelf" {
						t.Error("wrong account")
					}
					return object{"_": tt.constructor, "count": 2, "photos": []object{asObject(avatarFull(syntheticPNG(t))["profile_photo"])}, "users": []object{}}, 200
				}
				t.Error("unexpected asset read")
				return nil, 500
			})
			mediaClientSource(c, nil)
			out, err := c.Call(context.Background(), tt.name, json.RawMessage(tt.body))
			if err != nil || !strings.Contains(string(out), `"items":[{`) || strings.Contains(string(out), "access_hash") || strings.Contains(string(out), "private-reference") || strings.Contains(string(out), "file_reference") {
				t.Fatalf("unsafe projection %s %v", out, err)
			}
		})
	}
}
func TestAssetMutationsResolveFreshPrivateReferences(t *testing.T) {
	for _, name := range []string{"account.avatar.clear", "account.avatar.remove"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				switch method {
				case "photos.getUserPhotos":
					if asObject(p["user_id"]).str("_") != "inputUserSelf" {
						t.Error("foreign avatar lookup")
					}
					return object{"_": "photos.photos", "photos": []object{asObject(avatarFull(syntheticPNG(t))["profile_photo"])}, "users": []object{}}, 200
				case "photos.updateProfilePhoto":
					if asObject(p["id"]).str("_") != "inputPhotoEmpty" {
						t.Error("wrong clear")
					}
					return object{"_": "photos.photo", "photo": object{"_": "photoEmpty", "id": 0}, "users": []object{}}, 200
				case "photos.deletePhotos":
					photos := asObjects(p["id"])
					if len(photos) != 1 || photos[0].num("id") != 93 || photos[0].num("access_hash") != 94 {
						t.Error("unsafe photo reference")
					}
					return object{"_": "fixture.vector", "items": []int64{93}}, 200
				}
				t.Error(method)
				return nil, 500
			})
			mediaClientSource(c, nil)
			body := `{"request_id":"900"}`
			if name == "account.avatar.remove" {
				body = `{"request_id":"900","photo_id":"93"}`
			}
			out, err := c.Call(context.Background(), name, json.RawMessage(body))
			if err != nil || string(out) != `{"acknowledged":true}` || calls < 1 || calls > 2 {
				t.Fatalf("mutation %s %v calls=%d", out, err, calls)
			}
		})
	}
}
func TestAssetMutationRejectsForeignOrMalformedReference(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method != "photos.getUserPhotos" {
			t.Error("unsafe write")
		}
		return object{"_": "photos.photos", "photos": []object{}, "users": []object{}}, 200
	})
	mediaClientSource(c, nil)
	_, err := c.Call(context.Background(), "account.avatar.remove", json.RawMessage(`{"photo_id":"93","request_id":"900"}`))
	var de *domains.Error
	if !errors.As(err, &de) || de.Code != "AVATAR_NOT_FOUND" {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"photo_id":"9223372036854775808"}`, `{"photo_id":"93","access_hash":"777"}`} {
		if _, _, err := (Contract{}).NormalizeOperation("account.avatar.remove", json.RawMessage(raw)); err == nil {
			t.Fatal("unsafe asset accepted")
		}
	}
}
