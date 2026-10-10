package rubikameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAvatarListExposesOnlyOpaqueIDs(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, p object, _ bool) object {
		if method != "getAvatars" || p.str("object_guid") != "u0self" {
			t.Fatal("wrong avatar lookup")
		}
		return object{"avatars": []object{{"avatar_id": "opaque-photo", "main": object{"access_hash": "private", "file_id": "private", "dc_id": "private"}, "thumbnail": object{"file_id": "private"}}}}
	})
	raw, err := c.Call(context.Background(), "avatar.list", json.RawMessage(`{"peer":{"type":"user","id":"u0self"}}`))
	if err != nil || string(raw) != `{"complete":false,"items":[{"avatar_id":"opaque-photo"}]}` {
		t.Fatalf("invalid public avatar list: %s %v", raw, err)
	}
	if strings.Contains(string(raw), "private") {
		t.Fatal("private avatar reference leaked")
	}
}
func TestAvatarListRejectsMissingOrDuplicateIDs(t *testing.T) {
	for _, rows := range [][]object{{{}}, {{"avatar_id": "same"}, {"avatar_id": "same"}}} {
		c, _ := newRPCFixture(t, func(string, object, bool) object { return object{"avatars": rows} })
		if _, err := c.Call(context.Background(), "avatar.list", json.RawMessage(`{"peer":{"type":"user","id":"u0self"}}`)); err == nil {
			t.Fatal("invalid avatar identity accepted")
		}
	}
}
