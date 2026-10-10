package rubikameow

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"

	"github.com/mimalef70/goomni/src/domains"
	"golang.org/x/image/draw"
)

func (c *Client) listAvatars(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Peer domains.Peer `json:"peer"`
	}
	if json.Unmarshal(raw, &p) != nil || (Contract{}).ValidatePeer(p.Peer) != nil {
		return nil, domains.E("INVALID_PEER", "a valid avatar peer is required", 400)
	}
	if p.Peer.Type != "user" && p.Peer.Type != "group" && p.Peer.Type != "channel" {
		return nil, domains.Unsupported("avatar list requires user, group or channel")
	}
	result, err := c.invoke(ctx, "getAvatars", object{"object_guid": p.Peer.ID}, false, "")
	if err != nil {
		return nil, err
	}
	rows, ok := result["avatars"].([]any)
	if !ok || len(rows) > 100 {
		return nil, protocolError()
	}
	items := []object{}
	seen := map[string]bool{}
	for _, value := range rows {
		id := asObject(value).str("avatar_id")
		if !domains.ValidOpaqueID(id) || len(id) > 128 || seen[id] {
			return nil, protocolError()
		}
		seen[id] = true
		items = append(items, object{"avatar_id": id})
	}
	// The provider supplies no reviewed pagination/completeness witness.
	return json.Marshal(object{"items": items, "complete": false})
}

// Avatar writes use the same two inspected crop sizes observed in the web
// client. Each remote upload is journaled separately before the final mutation.
func (c *Client) avatarMutation(ctx context.Context, name string, raw json.RawMessage, rid string) (json.RawMessage, bool, error) {
	if name != "account.avatar" && name != "group.photo" && name != "avatar.remove" {
		return nil, false, nil
	}
	var p struct {
		Peer     domains.Peer `json:"peer"`
		MediaID  string       `json:"media_id"`
		AvatarID string       `json:"avatar_id"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, true, domains.E("INVALID_REQUEST", "invalid avatar request", 400)
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	if name == "account.avatar" {
		p.Peer = domains.Peer{Type: "user", ID: account}
	}
	if err := (Contract{}).ValidatePeer(p.Peer); err != nil {
		return nil, true, err
	}
	if p.Peer.Type == "user" && p.Peer.ID != account {
		return nil, true, domains.E("INVALID_PEER", "only the selected account's avatar may be changed", 400)
	}
	if name == "group.photo" && p.Peer.Type != "group" && p.Peer.Type != "channel" {
		return nil, true, domains.E("INVALID_PEER", "group photo requires group or channel", 400)
	}
	if !validMessageID(rid) {
		return nil, true, domains.E("INVALID_REQUEST_ID", "persist a request ID before changing an avatar", 400)
	}
	params := object{"object_guid": p.Peer.ID}
	method := "uploadAvatar"
	number := 3
	if name == "avatar.remove" {
		if !domains.ValidOpaqueID(p.AvatarID) {
			return nil, true, domains.E("INVALID_REQUEST", "avatar ID is required", 400)
		}
		result, err := c.invoke(ctx, "getAvatars", object{"object_guid": p.Peer.ID}, false, "")
		if err != nil {
			return nil, true, err
		}
		rows, ok := result["avatars"].([]any)
		if !ok || len(rows) > 100 {
			return nil, true, protocolError()
		}
		found := false
		for _, row := range rows {
			if asObject(row).str("avatar_id") == p.AvatarID {
				found = true
				break
			}
		}
		if !found {
			return nil, true, avatarUnavailable()
		}
		params["avatar_id"] = p.AvatarID
		method = "deleteAvatar"
		number = 1
	} else {
		images, err := c.avatarImages(ctx, p.MediaID)
		if err != nil {
			return nil, true, err
		}
		for i, body := range images {
			if err = stage(ctx, i+1, "upload", "started", rid, nil); err != nil {
				if i > 0 {
					err = uncertain(err)
				}
				return nil, true, err
			}
			uploaded, err := c.upload(ctx, bytes.NewReader(body), int64(len(body)), "avatar.jpg", "jpg")
			if err != nil {
				_ = stage(ctx, i+1, "upload", "unknown", rid, nil)
				return nil, true, uncertain(err)
			}
			if err = stage(ctx, i+1, "upload", "succeeded", rid, uploaded); err != nil {
				return nil, true, uncertain(err)
			}
			key := "thumbnail_file_id"
			if i == 1 {
				key = "main_file_id"
			}
			params[key] = uploaded.str("file_id")
		}
	}
	if err := stage(ctx, number, "profile.photo", "started", rid, nil); err != nil {
		if number > 1 {
			err = uncertain(err)
		}
		return nil, true, err
	}
	result, err := c.invoke(ctx, method, params, true, "")
	if err == nil {
		key := map[string]string{"user": "user_guid", "group": "group_guid", "channel": "channel_guid"}[p.Peer.Type]
		returned := asObject(result[p.Peer.Type])
		valid := returned != nil && returned.str(key) == p.Peer.ID
		if !valid {
			err = uncertain(protocolError())
		}
	}
	if err != nil {
		_ = stage(ctx, number, "profile.photo", "unknown", rid, nil)
		return nil, true, uncertain(err)
	}
	if err = stage(ctx, number, "profile.photo", "succeeded", rid, nil); err != nil {
		return nil, true, uncertain(err)
	}
	out, err := json.Marshal(object{"updated": true})
	return out, true, err
}
func (c *Client) avatarImages(ctx context.Context, id string) ([][]byte, error) {
	if c.cfg.MediaSource == nil || !domains.ValidOpaqueID(id) {
		return nil, domains.E("MEDIA_REQUIRED", "account-scoped image media is required", 400)
	}
	source, info, err := c.cfg.MediaSource(ctx, id)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, domains.E("MEDIA_NOT_FOUND", "media is unavailable", 404)
	}
	defer source.Close()
	limit := min(int64(8<<20), c.cfg.MaxMediaBytes)
	if info.Size < 1 || info.Size > limit {
		return nil, domains.E("AVATAR_TOO_LARGE", "avatar source exceeds the size limit", 413)
	}
	body, err := io.ReadAll(io.LimitReader(source, info.Size+1))
	if err != nil || int64(len(body)) != info.Size {
		return nil, domains.E("INVALID_MEDIA", "avatar source size changed", 400)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || (format != "jpeg" && format != "png" && format != "gif") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 16777216 {
		return nil, domains.E("INVALID_MEDIA", "avatar must be a bounded JPEG, PNG or GIF", 400)
	}
	src, actual, err := image.Decode(bytes.NewReader(body))
	if err != nil || actual != format {
		return nil, domains.E("INVALID_MEDIA", "invalid avatar image", 400)
	}
	b := src.Bounds()
	if b.Empty() || b.Min.X < 0 || b.Min.Y < 0 || b.Max.X > cfg.Width || b.Max.Y > cfg.Height || (format != "gif" && (b.Dx() != cfg.Width || b.Dy() != cfg.Height)) {
		return nil, domains.E("INVALID_MEDIA", "invalid avatar bounds", 400)
	}
	side := min(b.Dx(), b.Dy())
	x, y := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	crop := image.Rect(x, y, x+side, y+side)
	result := make([][]byte, 0, 2)
	for _, size := range []int{200, 800} {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		dst := image.NewRGBA(image.Rect(0, 0, size, size))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
		var output bytes.Buffer
		if err = jpeg.Encode(&output, dst, &jpeg.Options{Quality: 85}); err != nil {
			return nil, domains.E("INVALID_MEDIA", "avatar conversion failed", 400)
		}
		if int64(output.Len()) > c.cfg.MaxMediaBytes {
			return nil, domains.E("MEDIA_TOO_LARGE", "avatar rendition exceeds configured limit", 413)
		}
		result = append(result, output.Bytes())
	}
	return result, nil
}
