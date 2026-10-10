package eitaameow

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/mediautil"
)

func invalidMedia() error {
	return domains.E("INVALID_MEDIA", "media bytes or inspected metadata are invalid or unsupported", 400)
}
func compoundMediaError(err error) error {
	var e *domains.Error
	if errors.As(err, &e) && e.Ambiguous {
		return err
	}
	return &domains.Error{Code: "MEDIA_COMPOUND_UNKNOWN", Message: "an Eitaa media upload completed before the compound operation failed; automatic retry is disabled", HTTP: 502, Ambiguous: true}
}

// Only inspected formats are admitted as audio/video. Other formats may be
// sent as ordinary files, without invented duration or dimensions.
func prepareMedia(ctx context.Context, source io.Reader, size int64, kind string) (io.Reader, []object, string, func(), error) {
	cleanup := func() {}
	if kind == "file" {
		return source, nil, "", cleanup, nil
	}
	if kind == "voice" || kind == "audio" {
		reader, ms, done, err := mediautil.PrepareVoiceSource(ctx, source, size)
		if err != nil {
			return nil, nil, "", done, err
		}
		attr := object{"_": "documentAttributeAudio", "duration": (int64(ms) + 999) / 1000}
		if kind == "voice" {
			attr["voice"] = true
		}
		return reader, []object{attr}, "audio/ogg", done, nil
	}
	r, done, err := mediautil.SeekableSource(ctx, source, size)
	if err != nil {
		return nil, nil, "", done, err
	}
	fail := func() (io.Reader, []object, string, func(), error) {
		done()
		return nil, nil, "", cleanup, invalidMedia()
	}
	if kind == "image" {
		cfg, format, e := image.DecodeConfig(r)
		if e != nil || (format != "jpeg" && format != "png") || !boundedImage(cfg.Width, cfg.Height) {
			return fail()
		}
		if _, e = r.Seek(0, io.SeekStart); e != nil {
			return fail()
		}
		decoded, actual, e := image.Decode(r)
		if e != nil || actual != format || decoded.Bounds().Dx() != cfg.Width || decoded.Bounds().Dy() != cfg.Height {
			return fail()
		}
		if _, e = r.Seek(0, io.SeekStart); e != nil {
			return fail()
		}
		return r, nil, "image/" + format, done, nil
	}
	if kind == "video" {
		meta, e := mediautil.InspectMP4(r, size)
		if e != nil {
			return fail()
		}
		if _, e = r.Seek(0, io.SeekStart); e != nil {
			return fail()
		}
		return r, []object{{"_": "documentAttributeVideo", "supports_streaming": true, "duration": meta.DurationSeconds, "w": meta.Width, "h": meta.Height}}, "video/mp4", done, nil
	}
	return fail()
}
func boundedImage(w, h int) bool {
	return w > 0 && h > 0 && w <= 8192 && h <= 8192 && int64(w)*int64(h) <= 16777216
}

type albumItem struct {
	MediaID string `json:"media_id"`
	Kind    string `json:"kind"`
	Caption string `json:"caption,omitempty"`
}

func mediaChildRID(parent int64, index int) int64 {
	var p [16]byte
	binary.BigEndian.PutUint64(p[:8], uint64(parent))
	binary.BigEndian.PutUint64(p[8:], uint64(index))
	sum := sha256.Sum256(append([]byte("peykbridge:eitaa:album:v1:"), p[:]...))
	n := int64(binary.BigEndian.Uint64(sum[:8]) & math.MaxInt64)
	if n == 0 {
		return 1
	}
	return n
}
func (c *Client) sendAlbum(ctx context.Context, peer domains.Peer, items []albumItem, reply, requestID string) (_ json.RawMessage, err error) {
	rid, e := strconv.ParseInt(requestID, 10, 64)
	if e != nil || rid <= 0 {
		return nil, domains.E("INVALID_REQUEST_ID", "persist a positive int64 request ID before sending", 400)
	}
	if len(items) < 2 || len(items) > 10 {
		return nil, domains.E("INVALID_REQUEST", "album requires 2 to 10 items", 400)
	}
	for _, item := range items {
		if item.MediaID == "" || (item.Kind != "image" && item.Kind != "video") || !utf8.ValidString(item.Caption) || len([]rune(item.Caption)) > 4096 {
			return nil, domains.E("INVALID_REQUEST", "invalid album item", 400)
		}
	}
	if reply != "" {
		if _, e = messageNumber(reply); e != nil {
			return nil, e
		}
	}
	input, e := c.inputPeer(ctx, peer)
	if e != nil {
		return nil, e
	}
	accepted := false
	defer func() {
		if accepted && err != nil {
			err = compoundMediaError(err)
		}
	}()
	multi := make([]object, 0, len(items))
	ids := make([]int64, 0, len(items))
	for i, item := range items {
		id := mediaChildRID(rid, i)
		ids = append(ids, id)
		media, e := c.uploadMediaAt(ctx, domains.SendRequest{Peer: peer, Kind: item.Kind, MediaID: item.MediaID}, id, 2*i+1)
		if e != nil {
			return nil, e
		}
		accepted = true
		key, constructor, target := "photo", "inputPhoto", "inputMediaPhoto"
		if item.Kind == "video" {
			key, constructor, target = "document", "inputDocument", "inputMediaDocument"
		}
		prepared, e := c.mediaStage(ctx, 2*i+2, "upload", id, func() (object, error) {
			prepared, e := c.invokeUpload(ctx, "messages.uploadMedia", object{"peer": input, "media": media})
			if e != nil {
				return nil, e
			}
			file := asObject(prepared[key])
			ref, ok := file["file_reference"].([]byte)
			if prepared.str("_") != "messageMedia"+map[string]string{"photo": "Photo", "document": "Document"}[key] || file.num("id") == 0 || !ok || len(ref) > 64<<10 {
				return nil, compoundMediaError(protocolError())
			}
			if _, ok = file["access_hash"]; !ok {
				return nil, compoundMediaError(protocolError())
			}
			return prepared, nil
		})
		if e != nil {
			return nil, e
		}
		file := asObject(prepared[key])
		ref := file["file_reference"].([]byte)
		multi = append(multi, object{"_": "inputSingleMedia", "media": object{"_": target, "id": object{"_": constructor, "id": file.num("id"), "access_hash": file.num("access_hash"), "file_reference": ref}}, "random_id": id, "message": item.Caption})
	}
	params := object{"peer": input, "multi_media": multi}
	if reply != "" {
		params["reply_to_msg_id"], _ = messageNumber(reply)
	}
	response, e := c.mediaStage(ctx, 2*len(items)+1, "album.send", rid, func() (object, error) {
		response, e := c.invokeUpload(ctx, "messages.sendMultiMedia", params)
		if e != nil {
			return nil, e
		}
		for _, id := range ids {
			if _, e = sendResult(response, id); e != nil {
				return nil, e
			}
		}
		return response, nil
	})
	if e != nil {
		return nil, e
	}
	results := make([]domains.SendResult, 0, len(ids))
	for _, id := range ids {
		r, e := sendResult(response, id)
		if e != nil {
			return nil, e
		}
		results = append(results, r)
	}
	return json.Marshal(object{"items": results})
}

// Every write phase is durably started before contacting the provider. Journal
// failures after contact are uncertain writes and must stop the compound flow.
func (c *Client) mediaStage(ctx context.Context, number int, name string, nonce int64, run func() (object, error)) (object, error) {
	return c.mediaStageData(ctx, number, name, nonce, nil, run)
}

func (c *Client) mediaStageData(ctx context.Context, number int, name string, nonce int64, data json.RawMessage, run func() (object, error)) (object, error) {
	stage := domains.OperationStage{Number: number, Name: name, State: "started", Nonce: strconv.FormatInt(nonce, 10), Data: data}
	if err := domains.RecordOperationStage(ctx, stage); err != nil {
		return nil, err
	}
	result, err := run()
	stage.State = "succeeded"
	if err != nil {
		stage.State = "failed"
		var de *domains.Error
		if errors.As(err, &de) && de.Ambiguous {
			stage.State = "unknown"
		}
	}
	if recordErr := domains.RecordOperationStage(ctx, stage); recordErr != nil {
		return nil, compoundMediaError(recordErr)
	}
	return result, err
}
