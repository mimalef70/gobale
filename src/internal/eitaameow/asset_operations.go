package eitaameow

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/mimalef70/goomni/src/domains"
)

func assetOperations() []domains.OperationContract {
	result := []domains.OperationContract{}
	add := func(name, mode string, p map[string]domains.FieldSchema, required ...string) {
		result = append(result, domains.OperationContract{Operation: name, Mode: mode, Path: "/operations/" + name, Method: "POST", Description: "Eitaa " + name, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: p, Required: required}})
	}
	add("account.avatars", "read", map[string]domains.FieldSchema{"offset": boundedInt(0, 100000), "limit": boundedInt(1, 100)})
	add("account.avatar.remove", "mutation", map[string]domains.FieldSchema{"photo_id": {Type: "string", MinLength: 1, MaxLength: 20, Pattern: "^-?[1-9][0-9]*$"}}, "photo_id")
	add("account.avatar.clear", "mutation", map[string]domains.FieldSchema{})
	return result
}

type assetRequest struct {
	PhotoID string `json:"photo_id"`
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
}

func normalizeAssets(name string, raw json.RawMessage) error {
	known := false
	for _, op := range assetOperations() {
		if op.Operation == name {
			known = true
			break
		}
	}
	if !known {
		return nil
	}
	var p assetRequest
	if json.Unmarshal(raw, &p) != nil {
		return domains.E("INVALID_REQUEST", "invalid asset request", 400)
	}
	for _, s := range []string{p.PhotoID} {
		if s != "" {
			n, e := strconv.ParseInt(s, 10, 64)
			if e != nil || n == 0 || strconv.FormatInt(n, 10) != s {
				return domains.E("INVALID_REQUEST", "asset ID must be a canonical nonzero int64 string", 400)
			}
		}
	}
	return nil
}
func (c *Client) assetCall(ctx context.Context, name string, raw json.RawMessage, rid int64) (json.RawMessage, bool, error) {
	known := false
	for _, op := range assetOperations() {
		if op.Operation == name {
			known = true
			break
		}
	}
	if !known {
		return nil, false, nil
	}
	var p assetRequest
	if e := json.Unmarshal(raw, &p); e != nil {
		return nil, true, e
	}
	method := ""
	params := object{"hash": 0}
	write := false
	switch name {
	case "account.avatars":
		limit := p.Limit
		if limit == 0 {
			limit = 50
		}
		result, e := c.invoke(ctx, "photos.getUserPhotos", object{"user_id": object{"_": "inputUserSelf"}, "offset": p.Offset, "max_id": 0, "limit": limit}, false, false)
		if e != nil {
			var de *domains.Error
			if errors.As(e, &de) && de.Code == "AVATAR_NOT_FOUND" {
				b, err := json.Marshal(object{"items": []object{}, "complete": false, "has_more": false})
				return b, true, err
			}
			return nil, true, e
		}
		if result.str("_") != "photos.photos" && result.str("_") != "photos.photosSlice" {
			return nil, true, protocolError()
		}
		photos := asObjects(result["photos"])
		if len(photos) > limit {
			return nil, true, protocolError()
		}
		items := []object{}
		for _, photo := range photos {
			if photo.str("_") != "photo" || photo.num("id") == 0 {
				continue
			}
			items = append(items, object{"photo_id": strconv.FormatInt(photo.num("id"), 10), "date": photo.num("date")})
		}
		out := object{"items": items, "complete": false, "has_more": len(photos) == limit}
		if len(photos) == limit {
			out["next_offset"] = p.Offset + len(photos)
		}
		b, e := json.Marshal(out)
		return b, true, e
	case "account.avatar.clear":
		method = "photos.updateProfilePhoto"
		params = object{"id": object{"_": "inputPhotoEmpty"}}
		write = true
	case "account.avatar.remove":
		result, e := c.invoke(ctx, "photos.getUserPhotos", object{"user_id": object{"_": "inputUserSelf"}, "offset": 0, "max_id": 0, "limit": 100}, false, false)
		if e != nil {
			return nil, true, e
		}
		if result.str("_") != "photos.photos" && result.str("_") != "photos.photosSlice" {
			return nil, true, protocolError()
		}
		if len(asObjects(result["photos"])) > 100 {
			return nil, true, protocolError()
		}
		id, _ := strconv.ParseInt(p.PhotoID, 10, 64)
		var found object
		for _, photo := range asObjects(result["photos"]) {
			if photo.num("id") == id {
				if found != nil {
					return nil, true, protocolError()
				}
				found = photo
			}
		}
		if found == nil {
			return nil, true, avatarUnavailable()
		}
		reference, ok := found["file_reference"].([]byte)
		_, hashOK := found["access_hash"]
		if found.str("_") != "photo" || !ok || len(reference) > 64<<10 || !hashOK {
			return nil, true, protocolError()
		}
		method = "photos.deletePhotos"
		params = object{"id": []object{{"_": "inputPhoto", "id": id, "access_hash": found.num("access_hash"), "file_reference": reference}}}
		write = true
	}
	result, e := c.invoke(ctx, method, params, write, false)
	if e != nil {
		return nil, true, e
	}
	if write {
		accepted := result["acknowledged"] == true
		if name == "account.avatar.clear" {
			accepted = result.str("_") == "photos.photo" && asObject(result["photo"]).str("_") == "photoEmpty"
		}
		if name == "account.avatar.remove" {
			ids, _ := result["items"].([]any)
			if len(ids) != 1 {
				return nil, true, &domains.Error{Code: "SEND_UNKNOWN", Message: "Eitaa photo removal acknowledgement is incomplete", HTTP: 502, Ambiguous: true}
			}
			for _, id := range ids {
				if n, e := integer(id); e == nil && strconv.FormatInt(n, 10) == p.PhotoID {
					accepted = true
				}
			}
		}
		if !accepted {
			return nil, true, &domains.Error{Code: "SEND_UNKNOWN", Message: "Eitaa asset mutation acknowledgement is incomplete", HTTP: 502, Ambiguous: true}
		}
		b, e := json.Marshal(object{"acknowledged": true})
		return b, true, e
	}
	return nil, true, protocolError()
}
