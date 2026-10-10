package eitaameow

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestOfficialErrorVariantLiteralAndSafeMapping(t *testing.T) {
	c, _ := bundledCodec()
	// Independent TL bytes: constructor -404, code 400, PHONE_CODE_INVALID.
	raw, _ := hex.DecodeString("6cfeffff900100001250484f4e455f434f44455f494e56414c494400")
	v, err := c.decodeResponse("auth.signIn", raw)
	if err != nil {
		t.Fatal(err)
	}
	o := asObject(v)
	if o.str("_") != "eitta_error" {
		t.Fatal("error variant lost")
	}
	var public *domains.Error
	if !errors.As(rpcError(o), &public) || public.Code != "INVALID_CODE" {
		t.Fatal("incorrect error mapping")
	}
	client, _ := nativeFixture(t, func(string, object) (object, int) {
		return object{"_": "eitta_error", "code": 420, "text": "FLOOD_WAIT_60"}, 200
	})
	_, err = client.StartAuth(context.Background(), "+10000000000")
	if !errors.As(err, &public) || public.Code != "RATE_LIMITED" || public.RetryAfterSeconds != 60 {
		t.Fatalf("rate limit lost: %v", err)
	}
}

func TestObservedUnavailableMethodAndMissingPhotoDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		text, code string
		http       int
	}{{"INVALID_CONSTRUCTOR", "FEATURE_NOT_SUPPORTED", 501}, {"NO_PROFILE_PHOTO", "AVATAR_NOT_FOUND", 404}} {
		var de *domains.Error
		if !errors.As(rpcError(object{"text": tt.text}), &de) || de.Code != tt.code || de.HTTP != tt.http {
			t.Fatal("observed diagnostic mapping failed")
		}
	}
	c, _ := nativeFixture(t, func(method string, _ object) (object, int) {
		if method != "photos.getUserPhotos" {
			t.Fatal("unexpected method")
		}
		return object{"_": "eitta_error", "code": 400, "text": "NO_PROFILE_PHOTO"}, 200
	})
	mediaClientSource(c, nil)
	raw, handled, err := c.assetCall(context.Background(), "account.avatars", json.RawMessage(`{}`), 0)
	if err != nil || !handled || string(raw) != `{"complete":false,"has_more":false,"items":[]}` {
		t.Fatalf("missing avatar list was not represented safely: %s %v", raw, err)
	}
}
