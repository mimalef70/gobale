package eitaameow

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/jpeg"
	"os"
	"strconv"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestVideoPreviewUploadUsesInspectedPixelsAndJournaledIdentity(t *testing.T) {
	body, err := os.ReadFile("../mediautil/testdata/preview.mp4")
	if err != nil {
		t.Fatal(err)
	}
	rid := int64(7812345)
	previewID := mediaPreviewRID(rid)
	var stages []domains.OperationStage
	calls := 0
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls++
		if len(stages) == 0 {
			t.Fatal("upload started without a journal")
		}
		var private object
		if json.Unmarshal(stages[0].Data, &private) != nil || private.str("thumbnail_request_id") != strconv.FormatInt(previewID, 10) {
			t.Fatal("thumbnail identity was not persisted before upload")
		}
		switch method {
		case "upload.saveFilePart":
			if p.num("totalFileSize") != int64(len(body)) || asObject(p["peer"]).num("user_id") != 42 {
				t.Fatal("thumbnail lost original size or peer scope")
			}
			part, ok := p["bytes"].([]byte)
			if !ok {
				t.Fatal("missing uploaded bytes")
			}
			if p.num("file_id") == rid {
				if !bytes.Equal(part, body) {
					t.Fatal("preview inspection changed the main upload position")
				}
			} else if p.num("file_id") == previewID {
				frame, err := jpeg.Decode(bytes.NewReader(part))
				if err != nil || frame.Bounds().Dx() != 128 || frame.Bounds().Dy() != 72 {
					t.Fatal("preview is not a bounded actual JPEG frame")
				}
				r, g, b, _ := frame.At(64, 36).RGBA()
				if b < 60000 || r > 3000 || g > 3000 {
					t.Fatal("synthetic blue source pixels changed")
				}
			} else {
				t.Fatal("unexpected upload identity")
			}
			return object{"_": "boolTrue"}, 200
		case "messages.sendMedia":
			media := asObject(p["media"])
			thumb := asObject(media["thumb"])
			attrs := asObjects(media["attributes"])
			if p.num("random_id") != rid || thumb.num("id") != previewID || thumb.num("parts") != 1 || attrs[0]["supports_streaming"] != true || attrs[0].num("duration") != 1 || attrs[0].num("w") != 160 || attrs[0].num("h") != 90 {
				t.Fatal("video metadata/thumbnail identity mismatch")
			}
			return object{"_": "updateShortSentMessage", "id": 8, "pts": 1, "pts_count": 1, "date": 100}, 200
		}
		t.Fatal("unexpected request")
		return nil, 500
	})
	mediaClientSource(c, body)
	ctx := domains.WithOperationStageRecorder(context.Background(), func(_ context.Context, s domains.OperationStage) error {
		stages = append(stages, s)
		return nil
	})
	if _, err = c.Send(ctx, mediaRequest("video")); err != nil || calls != 3 || len(stages) != 4 || !bytes.Equal(stages[0].Data, stages[1].Data) {
		t.Fatalf("video send: calls=%d stages=%d error=%v", calls, len(stages), err)
	}
}

func TestVideoThumbnailFailureAfterMainUploadStaysUnknown(t *testing.T) {
	body, err := os.ReadFile("../mediautil/testdata/preview.mp4")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c, _ := nativeFixture(t, func(method string, _ object) (object, int) {
		calls++
		if method != "upload.saveFilePart" || calls > 2 {
			t.Fatal("send/retry after an uncertain thumbnail upload")
		}
		if calls == 2 {
			return nil, 500
		}
		return object{"_": "boolTrue"}, 200
	})
	mediaClientSource(c, body)
	_, err = c.Send(context.Background(), mediaRequest("video"))
	var de *domains.Error
	if !errors.As(err, &de) || !de.Ambiguous || calls != 2 {
		t.Fatalf("uncertain compound upload: %v calls=%d", err, calls)
	}
}

func TestVideoPreviewJournalFailurePreventsAllUploads(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		t.Fatal("contacted provider before thumbnail identity was durable")
		return nil, 500
	})
	ctx := domains.WithOperationStageRecorder(context.Background(), func(_ context.Context, s domains.OperationStage) error {
		if len(s.Data) == 0 {
			t.Fatal("missing private preview identity")
		}
		return errors.New("synthetic commit failure")
	})
	if _, err := c.Send(ctx, mediaRequest("video")); err == nil {
		t.Fatal("journal failure ignored")
	}
}

func TestVideoAttributeLiteralStreamingLayout(t *testing.T) {
	c, err := bundledCodec()
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if err = c.encodeType(&raw, "DocumentAttribute", object{"_": "documentAttributeVideo", "supports_streaming": true, "duration": 5, "w": 320, "h": 180}, 0); err != nil {
		t.Fatal(err)
	}
	// Reviewed constructor, flags bit 1, integer duration and actual dimensions.
	if hex.EncodeToString(raw.Bytes()) != "e62cf00e020000000500000040010000b4000000" {
		t.Fatalf("video literal mismatch: %x", raw.Bytes())
	}
}
