package rubikameow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestAvatarMutationUploadsInspectedRenditionsAndJournals(t *testing.T) {
	body := avatarPNG(t)
	sizes := []int{}
	stages := []domains.OperationStage{}
	requests := 0
	mutations := 0
	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || format != "jpeg" || cfg.Width != cfg.Height {
			t.Error("uninspected avatar rendition")
		}
		sizes = append(sizes, cfg.Width)
		_, _ = w.Write([]byte(`{"status":"OK","status_det":"OK","data":{"access_hash_rec":"private"}}`))
	}))
	defer upload.Close()
	c, _ := newRPCFixture(t, func(method string, p object, _ bool) object {
		switch method {
		case "requestSendFile":
			requests++
			if len(stages) != 2*requests-1 || stages[len(stages)-1].State != "started" {
				t.Error("upload before journal")
			}
			return object{"id": strconv.Itoa(requests + 100), "dc_id": "4", "upload_url": upload.URL, "access_hash_send": "private-send"}
		case "uploadAvatar":
			mutations++
			if len(stages) != 5 || p.str("thumbnail_file_id") != "101" || p.str("main_file_id") != "102" || p.str("object_guid") != "u0self" {
				t.Errorf("incorrect final avatar mutation: %#v", p)
			}
			return object{"user": object{"user_guid": "u0self"}}
		}
		t.Error(method)
		return nil
	})
	c.cfg.MediaSource = func(context.Context, string) (io.ReadCloser, domains.NativeMediaInfo, error) {
		return io.NopCloser(bytes.NewReader(body)), domains.NativeMediaInfo{Size: int64(len(body))}, nil
	}
	ctx := domains.WithOperationStageRecorder(context.Background(), func(_ context.Context, s domains.OperationStage) error { stages = append(stages, s); return nil })
	raw, handled, err := c.avatarMutation(ctx, "account.avatar", json.RawMessage(`{"media_id":"owned"}`), "500")
	if err != nil || !handled || string(raw) != `{"updated":true}` || mutations != 1 || len(sizes) != 2 || sizes[0] != 200 || sizes[1] != 800 || len(stages) != 6 || stages[5].State != "succeeded" {
		t.Fatalf("avatar result %s %v sizes=%v stages=%d", raw, err, sizes, len(stages))
	}
}
func TestAvatarMutationRejectsBeforeWriteAndPreservesUncertainty(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { t.Error("unexpected provider call"); return nil })
	c.cfg.MediaSource = func(context.Context, string) (io.ReadCloser, domains.NativeMediaInfo, error) {
		return io.NopCloser(bytes.NewReader([]byte("bad"))), domains.NativeMediaInfo{Size: 3}, nil
	}
	if _, _, err := c.avatarMutation(context.Background(), "account.avatar", json.RawMessage(`{"media_id":"owned"}`), "500"); err == nil {
		t.Fatal("corrupt avatar accepted")
	}
	if _, _, err := c.avatarMutation(context.Background(), "avatar.remove", json.RawMessage(`{"peer":{"type":"user","id":"u0other"},"avatar_id":"1"}`), "500"); err == nil {
		t.Fatal("cross-account mutation accepted")
	}
	calls := 0
	deleted := false
	c, _ = newRPCFixture(t, func(method string, p object, _ bool) object {
		calls++
		if method == "getAvatars" {
			return object{"avatars": []any{object{"avatar_id": "opaque-avatar"}}}
		}
		if method == "deleteAvatar" {
			deleted = true
			if p.str("object_guid") != "g0group" || p.str("avatar_id") != "opaque-avatar" {
				t.Error("delete scope")
			}
			return nil
		}
		t.Error(method)
		return nil
	})
	var stages []domains.OperationStage
	ctx := domains.WithOperationStageRecorder(context.Background(), func(_ context.Context, s domains.OperationStage) error { stages = append(stages, s); return nil })
	_, _, err := c.avatarMutation(ctx, "avatar.remove", json.RawMessage(`{"peer":{"type":"group","id":"g0group"},"avatar_id":"opaque-avatar"}`), "500")
	var de *domains.Error
	if !errors.As(err, &de) || !de.Ambiguous || !deleted || calls != 2 || len(stages) != 2 || stages[1].State != "unknown" {
		t.Fatalf("uncertainty lost: %v %+v", err, stages)
	}
}

func TestAvatarMutationRejectsWrongAcknowledgedIdentity(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, p object, _ bool) object {
		if method == "getAvatars" {
			return object{"avatars": []any{object{"avatar_id": "photo"}}}
		}
		if method == "deleteAvatar" {
			return object{"user": object{"user_guid": "u0other"}}
		}
		t.Error(method)
		return nil
	})
	var states []string
	ctx := domains.WithOperationStageRecorder(context.Background(), func(_ context.Context, s domains.OperationStage) error { states = append(states, s.State); return nil })
	_, _, err := c.avatarMutation(ctx, "avatar.remove", json.RawMessage(`{"peer":{"type":"user","id":"u0self"},"avatar_id":"photo"}`), "500")
	var de *domains.Error
	if !errors.As(err, &de) || !de.Ambiguous || len(states) != 2 || states[1] != "unknown" {
		t.Fatalf("wrong account acknowledged: %v %v", err, states)
	}
}
