package eitaameow

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"sync/atomic"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func avatarFull(body []byte) object {
	return object{"_": "userFull", "user": object{"_": "user", "id": 42}, "settings": object{"_": "peerSettings"}, "notify_settings": object{"_": "peerNotifySettings"}, "common_chats_count": 0, "profile_photo": object{"_": "photo", "id": 93, "access_hash": 94, "file_reference": []byte{1, 2}, "date": 100, "dc_id": 1, "sizes": []object{{"_": "photoSize", "type": "m", "w": 2, "h": 3, "size": len(body), "location": object{"_": "fileLocation", "dc_id": 1, "volume_id": 95, "local_id": 96, "secret": 97}}}}}
}
func TestAvatarFreshPrivateResolutionAndInspectedBytes(t *testing.T) {
	body := syntheticPNG(t)
	var lookups, downloads atomic.Int32
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		switch method {
		case "users.getFullUser":
			if asObject(p["id"]).str("_") != "inputUserSelf" {
				t.Error("wrong account")
			}
			if lookups.Add(1) > 1 {
				return object{"_": "userFull", "user": object{"_": "user", "id": 42}, "settings": object{"_": "peerSettings"}, "notify_settings": object{"_": "peerNotifySettings"}, "common_chats_count": 0}, 200
			}
			return avatarFull(body), 200
		case "upload.getFile":
			downloads.Add(1)
			loc := asObject(p["location"])
			if loc.str("_") != "inputPhotoFileLocation" || loc.num("id") != 93 || loc.num("access_hash") != 94 || loc.str("thumb_size") != "m" {
				t.Error("lost authenticated reference")
			}
			return object{"_": "upload.file", "type": object{"_": "storage.filePng"}, "mtime": 100, "bytes": body}, 200
		}
		t.Error(method)
		return nil, 500
	})
	mediaClientSource(c, nil)
	peer := domains.Peer{Type: "user", ID: "42"}
	r, info, e := c.DownloadAvatar(context.Background(), peer, "large")
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(r)
	_ = r.Close()
	if e != nil || !bytes.Equal(got, body) || info.ContentType != "image/png" || info.Width != 2 || info.Height != 3 || info.Name != "avatar.png" {
		t.Fatalf("wrong avatar bytes/info: %#v %v", info, e)
	}
	_, _, e = c.DownloadAvatar(context.Background(), peer, "small")
	var de *domains.Error
	if !errors.As(e, &de) || de.Code != "AVATAR_NOT_FOUND" || lookups.Load() != 2 || downloads.Load() != 1 {
		t.Fatalf("stale avatar reused: %v", e)
	}
}
func TestAvatarCorruptionNeverBecomesSuccessfulStream(t *testing.T) {
	body := []byte("corrupt-image")
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method == "users.getFullUser" {
			return avatarFull(body), 200
		}
		return object{"_": "upload.file", "type": object{"_": "storage.filePng"}, "mtime": 100, "bytes": body}, 200
	})
	mediaClientSource(c, nil)
	r, _, e := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "42"}, "small")
	var de *domains.Error
	if r != nil || !errors.As(e, &de) || de.Code != "AVATAR_INVALID" {
		t.Fatalf("corrupt avatar accepted: %v", e)
	}
}
func TestAvatarWriteCompoundFailureIsUnknown(t *testing.T) {
	var calls atomic.Int32
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls.Add(1)
		if method == "upload.saveFilePart" {
			return object{"_": "boolTrue"}, 200
		}
		if method == "photos.uploadProfilePhoto" {
			return object{"_": "error", "code": 400, "text": "PHOTO_INVALID"}, 200
		}
		t.Error(method)
		return nil, 500
	})
	mediaClientSource(c, syntheticPNG(t))
	_, e := c.setPhoto(context.Background(), "account.avatar", domains.Peer{}, "local", "2345")
	var de *domains.Error
	if !errors.As(e, &de) || !de.Ambiguous || calls.Load() != 2 {
		t.Fatalf("compound mutation retried or reported definite failure: %v calls=%d", e, calls.Load())
	}
}

func TestProfilePhotoNormalizesPNGBeforeUpload(t *testing.T) {
	var uploaded []byte
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		switch method {
		case "upload.saveFilePart":
			chunk, ok := p["bytes"].([]byte)
			if !ok || p.num("totalFileSize") != int64(len(chunk)) || p.num("file_id") != 2345 {
				t.Fatal("incorrect normalized upload metadata")
			}
			uploaded = append(uploaded, chunk...)
			return object{"_": "boolTrue"}, 200
		case "photos.uploadProfilePhoto":
			file := asObject(p["file"])
			if file.str("name") != "profile.jpg" || file.num("id") != 2345 {
				t.Fatal("photo did not use its journaled upload")
			}
			return object{"_": "photos.photo", "photo": avatarFull(uploaded)["profile_photo"], "users": []object{}}, 200
		}
		t.Fatalf("unexpected method %s", method)
		return nil, 500
	})
	mediaClientSource(c, syntheticPNG(t))
	if _, err := c.setPhoto(context.Background(), "account.avatar", domains.Peer{}, "local", "2345"); err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(uploaded))
	if err != nil || format != "jpeg" || cfg.Width != 512 || cfg.Height != 512 {
		t.Fatalf("invalid normalized photo: %v %s %v", cfg, format, err)
	}
}

func TestProfilePhotoRejectsInvalidOrCancelledInputBeforeWrite(t *testing.T) {
	for _, body := range [][]byte{[]byte("invalid"), {}} {
		c, _ := nativeFixture(t, func(string, object) (object, int) {
			t.Fatal("invalid photo reached provider")
			return nil, 500
		})
		mediaClientSource(c, body)
		if _, err := c.setPhoto(context.Background(), "account.avatar", domains.Peer{}, "local", "2345"); err == nil {
			t.Fatal("invalid image accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareProfilePhoto(ctx, bytes.NewReader(syntheticPNG(t)), int64(len(syntheticPNG(t))), 8<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
