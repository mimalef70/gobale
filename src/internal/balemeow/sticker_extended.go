package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

const imagesService = "bale.v1.Images"

// Sticker hashes and media locations are obtained from this account's provider
// inventory, never accepted from an API caller or shared across client instances.
func (c *Client) stickerExtended(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Peer         domains.Peer `json:"peer"`
		CollectionID string       `json:"collection_id"`
		StickerID    string       `json:"sticker_id"`
		Offset       string       `json:"offset"`
		Animated     bool         `json:"animated"`
		RequestID    string       `json:"request_id"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > 16<<10 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return nil, groupRequestError("invalid sticker operation body")
	}
	var rid int64
	var err error
	switch op {
	case "send.sticker", "sticker.pack.add", "sticker.pack.remove":
		rid, err = positiveID(p.RequestID)
		if err != nil {
			return nil, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before sticker mutation", 400)
		}
	case "sticker.list", "sticker.get":
	default:
		return nil, domains.Unsupported(op)
	}
	var collectionID, stickerID int32
	if op != "sticker.list" {
		collectionID, err = positiveInt32(p.CollectionID)
		if err != nil {
			return nil, groupRequestError("collection_id must be a positive decimal int32")
		}
	}
	if op == "send.sticker" {
		stickerID, err = positiveInt32(p.StickerID)
		if err != nil {
			return nil, groupRequestError("sticker_id must be a positive decimal int32")
		}
		if _, err = encodePeer(p.Peer); err != nil {
			return nil, err
		}
	}
	if len(p.Offset) > 4096 || !utf8.ValidString(p.Offset) {
		return nil, groupRequestError("offset must be a valid opaque string of at most 4096 bytes")
	}
	switch op {
	case "sticker.list":
		result, err := c.ownStickerPage(ctx, p.Offset)
		if err != nil {
			return nil, err
		}
		collections := make([]map[string]any, 0, len(result.OwnStickers))
		for _, pack := range result.OwnStickers {
			value, e := safeStickerCollection(pack)
			if e != nil {
				return nil, e
			}
			collections = append(collections, value)
		}
		return json.Marshal(map[string]any{"collections": collections, "next": result.GetOffset().GetValue()})
	case "sticker.pack.add", "sticker.pack.remove":
		method := "AddStickerPack"
		if op == "sticker.pack.remove" {
			method = "RemoveStickerPack"
		}
		data, err := c.rpc(ctx, imagesService, method, &wire.StickerPackRequest{Id: collectionID})
		if err != nil {
			return nil, err
		}
		if decode(data, &wire.Empty{}) != nil {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "collection_id": p.CollectionID})
	}
	pack, err := c.ownedStickerCollection(ctx, collectionID)
	if err != nil {
		return nil, err
	}
	if op == "sticker.get" {
		safe, err := safeStickerCollection(pack)
		if err != nil {
			return nil, err
		}
		return json.Marshal(safe)
	}
	message := &wire.Message{}
	if p.Animated {
		for _, sticker := range pack.AnimatedStickers {
			if sticker != nil && sticker.Id == stickerID {
				if !validStickerImage(sticker.FileLocation) {
					return nil, protocolError()
				}
				message.AnimatedSticker = &wire.AnimatedStickerMessage{Id: &wire.Int32Value{Value: stickerID}, FileLocation: sticker.FileLocation, CollectionId: &wire.Int32Value{Value: collectionID}, CollectionAccessHash: &wire.Int64Value{Value: pack.AccessHash}}
				break
			}
		}
	} else {
		for _, sticker := range pack.Stickers {
			if sticker != nil && sticker.Id == stickerID {
				if sticker.Format < 0 || sticker.Format > 2 {
					return nil, domains.Unsupported("sticker format")
				}
				if (sticker.Format == 0 && !validStickerImage(sticker.Image512) && !validStickerImage(sticker.Image256)) || (sticker.Format != 0 && !validStickerImage(sticker.Animation)) {
					return nil, protocolError()
				}
				for _, im := range []*wire.StickerImage{sticker.Image512, sticker.Image256, sticker.Animation} {
					if im != nil && !validStickerImage(im) {
						return nil, protocolError()
					}
				}
				message.Sticker = &wire.StickerMessage{Id: &wire.Int32Value{Value: stickerID}, Image512: sticker.Image512, Image256: sticker.Image256, CollectionId: &wire.Int32Value{Value: collectionID}, CollectionAccessHash: &wire.Int64Value{Value: pack.AccessHash}, Format: sticker.Format, Animation: sticker.Animation}
				break
			}
		}
	}
	if message.Sticker == nil && message.AnimatedSticker == nil {
		return nil, boundedError("STICKER_NOT_FOUND", "sticker is absent from this account's installed collection", 404)
	}
	peer, err := c.messagePeer(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	data, err := c.rpc(ctx, "bale.messaging.v2.Messaging", "SendMessage", &wire.SendMessageRequest{Peer: peer, ExPeer: extendedPeer(peer, p.Peer), Rid: rid, Message: message})
	if err != nil {
		return nil, err
	}
	response := &wire.SendMessageResponse{}
	if decode(data, response) != nil || response.Date <= 0 {
		return nil, ambiguous()
	}
	return json.Marshal(map[string]any{"acknowledged": true, "message_id": p.RequestID, "date": time.UnixMilli(response.Date).UTC(), "collection_id": p.CollectionID, "sticker_id": p.StickerID, "animated": p.Animated})
}

func positiveInt32(value string) (int32, error) {
	n, err := positiveID(value)
	if err != nil || n > 2147483647 {
		return 0, groupRequestError("a positive decimal int32 is required")
	}
	return int32(n), nil
}

func (c *Client) ownStickerPage(ctx context.Context, offset string) (*wire.StickerListResponse, error) {
	request := &wire.StickerListRequest{}
	if offset != "" {
		request.Offset = &wire.StringValue{Value: offset}
	}
	data, err := c.readRPC(ctx, imagesService, "LoadOwnStickers", request)
	if err != nil {
		return nil, err
	}
	response := &wire.StickerListResponse{}
	if decode(data, response) != nil || len(response.OwnStickers) > 1000 || len(response.GetOffset().GetValue()) > 4096 || !utf8.ValidString(response.GetOffset().GetValue()) {
		return nil, protocolError()
	}
	seen := map[int32]bool{}
	for _, pack := range response.OwnStickers {
		if pack == nil || pack.Id <= 0 || seen[pack.Id] {
			return nil, protocolError()
		}
		seen[pack.Id] = true
	}
	return response, nil
}

func (c *Client) ownedStickerCollection(ctx context.Context, id int32) (*wire.StickerCollection, error) {
	offset := ""
	seen := map[string]bool{}
	// Bounded traversal avoids unbounded provider work for a fabricated ID.
	for page := 0; page < 16; page++ {
		result, err := c.ownStickerPage(ctx, offset)
		if err != nil {
			return nil, err
		}
		for _, pack := range result.OwnStickers {
			if pack.Id == id {
				data, err := c.readRPC(ctx, imagesService, "LoadStickerCollection", &wire.StickerCollectionRequest{Id: id, AccessHash: pack.AccessHash})
				if err != nil {
					return nil, err
				}
				response := &wire.StickerCollectionResponse{}
				if decode(data, response) != nil || response.Collection == nil || response.Collection.Id != id {
					return nil, protocolError()
				}
				// Use the descriptor returned by this account's authenticated lookup.
				if _, err = safeStickerCollection(response.Collection); err != nil {
					return nil, err
				}
				return response.Collection, nil
			}
		}
		offset = result.GetOffset().GetValue()
		if offset == "" {
			return nil, boundedError("STICKER_COLLECTION_NOT_FOUND", "collection is not installed on this account", 404)
		}
		if seen[offset] {
			return nil, protocolError()
		}
		seen[offset] = true
	}
	return nil, boundedError("STICKER_LOOKUP_LIMIT", "installed sticker inventory exceeds the bounded lookup limit", 422)
}

func validStickerImage(image *wire.StickerImage) bool {
	return image != nil && image.File != nil && image.File.FileId != 0 && image.Width >= 0 && image.Width <= 32768 && image.Height >= 0 && image.Height <= 32768 && image.FileSize > 0 && image.FileSize <= 100<<20
}

func safeStickerCollection(pack *wire.StickerCollection) (map[string]any, error) {
	if pack == nil || pack.Id <= 0 || len(pack.Stickers)+len(pack.AnimatedStickers) > 2000 || len(pack.GetName().GetValue()) > 4096 || !utf8.ValidString(pack.GetName().GetValue()) {
		return nil, protocolError()
	}
	stickers := make([]map[string]any, 0, len(pack.Stickers)+len(pack.AnimatedStickers))
	seen := map[int32]bool{}
	for _, s := range pack.Stickers {
		if s == nil || s.Id <= 0 || seen[s.Id] || len(s.GetEmoji().GetValue()) > 256 || !utf8.ValidString(s.GetEmoji().GetValue()) {
			return nil, protocolError()
		}
		seen[s.Id] = true
		stickers = append(stickers, map[string]any{"id": strconv.FormatInt(int64(s.Id), 10), "emoji": s.GetEmoji().GetValue(), "format": s.Format, "animated": false})
	}
	seen = map[int32]bool{}
	for _, s := range pack.AnimatedStickers {
		if s == nil || s.Id <= 0 || seen[s.Id] || len(s.GetEmoji().GetValue()) > 256 || !utf8.ValidString(s.GetEmoji().GetValue()) {
			return nil, protocolError()
		}
		seen[s.Id] = true
		stickers = append(stickers, map[string]any{"id": strconv.FormatInt(int64(s.Id), 10), "emoji": s.GetEmoji().GetValue(), "animated": true})
	}
	return map[string]any{"id": strconv.FormatInt(int64(pack.Id), 10), "name": pack.GetName().GetValue(), "animated": pack.GetAnimated().GetValue(), "stickers": stickers}, nil
}
