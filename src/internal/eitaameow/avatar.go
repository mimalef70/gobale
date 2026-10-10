package eitaameow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"strconv"

	"github.com/mimalef70/goomni/src/domains"
)

func avatarUnavailable() error {
	return domains.E("AVATAR_NOT_FOUND", "contact avatar is unavailable", 404)
}
func avatarInvalid() error {
	return domains.E("AVATAR_INVALID", "provider avatar bytes or reference are invalid", 502)
}
func avatarError(err error) error {
	var e *domains.Error
	if errors.As(err, &e) && (e.HTTP == 403 || e.HTTP == 404) {
		return avatarUnavailable()
	}
	return err
}

// Resolve a fresh current profile photo using the selected authenticated client.
// Private hashes/references remain local and never become provider URLs or JSON.
func (c *Client) DownloadAvatar(ctx context.Context, user domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error) {
	if size == "" {
		size = "small"
	}
	if size != "small" && size != "large" {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_REQUEST", "avatar size must be small or large", 400)
	}
	if user.Type != "user" {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_PEER", "avatar requires a user peer", 400)
	}
	peer, err := c.inputPeer(ctx, user)
	if err != nil {
		return nil, domains.AvatarInfo{}, avatarError(err)
	}
	input := object{"_": "inputUser", "user_id": peer.num("user_id"), "access_hash": peer.num("access_hash")}
	if peer.str("_") == "inputPeerSelf" {
		input = object{"_": "inputUserSelf"}
	}
	response, err := c.invoke(ctx, "users.getFullUser", object{"id": input}, false, false)
	if err != nil {
		return nil, domains.AvatarInfo{}, avatarError(err)
	}
	if response.str("_") != "userFull" || strconv.FormatInt(asObject(response["user"]).num("id"), 10) != user.ID {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	photo := asObject(response["profile_photo"])
	if photo == nil || photo.str("_") == "photoEmpty" {
		return nil, domains.AvatarInfo{}, avatarUnavailable()
	}
	if photo.str("_") != "photo" || photo.num("id") == 0 {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	var selected object
	for _, candidate := range asObjects(photo["sizes"]) {
		if candidate.str("_") != "photoSize" || candidate.num("size") < 1 || candidate.str("type") == "" {
			continue
		}
		if !boundedImage(int(candidate.num("w")), int(candidate.num("h"))) {
			continue
		}
		if selected == nil || (size == "small" && candidate.num("size") < selected.num("size")) || (size == "large" && candidate.num("size") > selected.num("size")) {
			selected = candidate
		}
	}
	if selected == nil {
		return nil, domains.AvatarInfo{}, avatarUnavailable()
	}
	limit := min(int64(8<<20), c.cfg.MaxMediaBytes)
	length := selected.num("size")
	if length > limit {
		return nil, domains.AvatarInfo{}, domains.E("AVATAR_TOO_LARGE", "contact avatar exceeds the size limit", 413)
	}
	ref, ok := photo["file_reference"].([]byte)
	if !ok || len(ref) > 64<<10 {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	if _, ok = photo["access_hash"]; !ok {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	location := object{"_": "inputPhotoFileLocation", "id": photo.num("id"), "access_hash": photo.num("access_hash"), "file_reference": ref, "thumb_size": selected.str("type")}
	body := make([]byte, 0, int(length))
	for int64(len(body)) < length {
		remaining := min(int64(512<<10), length-int64(len(body)))
		response, err = c.invoke(ctx, "upload.getFile", object{"location": location, "offset": len(body), "limit": remaining}, false, false)
		if err != nil {
			return nil, domains.AvatarInfo{}, avatarError(err)
		}
		chunk, ok := response["bytes"].([]byte)
		if response.str("_") != "upload.file" || !ok || len(chunk) == 0 || int64(len(chunk)) > remaining {
			return nil, domains.AvatarInfo{}, avatarInvalid()
		}
		body = append(body, chunk...)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || !boundedImage(cfg.Width, cfg.Height) {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	mime := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif"}[format]
	if mime == "" {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	decoded, actual, err := image.Decode(bytes.NewReader(body))
	if err != nil || actual != format {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	bounds := decoded.Bounds()
	if bounds.Empty() || bounds.Min.X < 0 || bounds.Min.Y < 0 || bounds.Max.X > cfg.Width || bounds.Max.Y > cfg.Height || (format != "gif" && (bounds.Dx() != cfg.Width || bounds.Dy() != cfg.Height)) {
		return nil, domains.AvatarInfo{}, avatarInvalid()
	}
	ext := format
	if ext == "jpeg" {
		ext = "jpg"
	}
	info := domains.AvatarInfo{Name: "avatar." + ext, ContentType: mime, Size: int64(len(body)), Width: cfg.Width, Height: cfg.Height}
	return io.NopCloser(bytes.NewReader(body)), info, nil
}

// Photo changes upload an inspected still image, then execute exactly one
// mutation. A later failure leaves the compound operation unknown.
func (c *Client) setPhoto(ctx context.Context, operation string, peer domains.Peer, mediaID, requestID string) (_ json.RawMessage, err error) {
	rid, e := strconv.ParseInt(requestID, 10, 64)
	if e != nil || rid <= 0 {
		return nil, domains.E("INVALID_REQUEST_ID", "persist a positive int64 request ID before sending", 400)
	}
	var input object
	switch operation {
	case "account.avatar":
		c.mu.RLock()
		peer = domains.Peer{Type: "user", ID: c.session.UserID}
		c.mu.RUnlock()
	case "group.photo":
		if peer.Type != "group" && peer.Type != "channel" {
			return nil, domains.E("INVALID_PEER", "photo update requires a group or channel", 400)
		}
	default:
		return nil, domains.Unsupported(operation)
	}
	input, e = c.inputPeer(ctx, peer)
	if e != nil {
		return nil, e
	}
	media, e := c.uploadMedia(ctx, domains.SendRequest{Peer: peer, Kind: "profile_photo", MediaID: mediaID}, rid)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			err = compoundMediaError(err)
		}
	}()
	file := asObject(media["file"])
	method := "photos.uploadProfilePhoto"
	params := object{"file": file}
	if operation == "group.photo" {
		photo := object{"_": "inputChatUploadedPhoto", "file": file}
		if input.str("_") == "inputPeerChannel" {
			method = "channels.editPhoto"
			params = object{"channel": object{"_": "inputChannel", "channel_id": input.num("channel_id"), "access_hash": input.num("access_hash")}, "photo": photo}
		} else {
			method = "messages.editChatPhoto"
			params = object{"chat_id": input.num("chat_id"), "photo": photo}
		}
	}
	_, e = c.mediaStage(ctx, 2, "profile.photo", rid, func() (object, error) {
		result, e := c.invokeUpload(ctx, method, params)
		if e != nil {
			return nil, e
		}
		if operation == "account.avatar" {
			photo := asObject(result["photo"])
			if result.str("_") != "photos.photo" || photo.str("_") != "photo" || photo.num("id") == 0 {
				return nil, compoundMediaError(protocolError())
			}
		} else if result.str("_") != "updates" && result.str("_") != "updatesCombined" {
			return nil, compoundMediaError(protocolError())
		}
		return object{"updated": true}, nil
	})
	if e != nil {
		return nil, e
	}
	return json.Marshal(object{"updated": true})
}

// Eitaa's photo editor accepts a square JPEG. Ordinary message images keep
// their original bytes; only explicit profile/group photos are center-cropped
// and resized to the editor's 512-pixel square before upload.
func prepareProfilePhoto(ctx context.Context, source io.Reader, size, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if size < 1 || size > min(int64(8<<20), limit) {
		return nil, invalidMedia()
	}
	raw, err := io.ReadAll(io.LimitReader(source, size+1))
	if err != nil || int64(len(raw)) != size {
		return nil, invalidMedia()
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png") || !boundedImage(cfg.Width, cfg.Height) {
		return nil, invalidMedia()
	}
	original, actual, err := image.Decode(bytes.NewReader(raw))
	if err != nil || actual != format || original.Bounds().Dx() != cfg.Width || original.Bounds().Dy() != cfg.Height {
		return nil, invalidMedia()
	}
	w, h := 512, 512
	side := min(cfg.Width, cfg.Height)
	left, top := (cfg.Width-side)/2, (cfg.Height-side)/2
	normalized := image.NewRGBA(image.Rect(0, 0, w, h))
	bounds := original.Bounds()
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < w; x++ {
			r, g, b, a := original.At(bounds.Min.X+left+x*side/w, bounds.Min.Y+top+y*side/h).RGBA()
			// Composite transparency onto white before discarding alpha.
			normalized.SetRGBA(x, y, color.RGBA{uint8((r + 65535 - a) >> 8), uint8((g + 65535 - a) >> 8), uint8((b + 65535 - a) >> 8), 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, normalized, &jpeg.Options{Quality: 85}); err != nil || int64(encoded.Len()) > min(int64(8<<20), limit) {
		return nil, invalidMedia()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}
