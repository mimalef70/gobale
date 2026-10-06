package balemeow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"strconv"
)

func (c *Client) accountMagazineCall(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	var p struct {
		Peer      domains.Peer `json:"peer"`
		MessageID string       `json:"message_id"`
		DateMS    string       `json:"date_ms"`
		AlbumID   string       `json:"album_id"`
		Next      string       `json:"next"`
		RequestID string       `json:"request_id"`
	}
	if len(raw) > 16<<10 || decodeAccountBody(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid magazine operation", 400)
	}
	read := op == "message.upvoters"
	if !read {
		if err := requireAccountMutationID(p.RequestID); err != nil {
			return nil, err
		}
	}
	rid, err := messageID(p.MessageID)
	if err != nil {
		return nil, boundedError("INVALID_REQUEST", "message_id must be a nonzero signed int64 string", 400)
	}
	date, err := accountDate(p.DateMS)
	if err != nil {
		return nil, err
	}
	var album *wire.Int64Value
	if p.AlbumID != "" {
		id, err := messageID(p.AlbumID)
		if err != nil || read {
			return nil, boundedError("INVALID_REQUEST", "invalid album_id for this operation", 400)
		}
		album = &wire.Int64Value{Value: id}
	}
	if len(p.Next) > 5464 || (!read && p.Next != "") {
		return nil, boundedError("INVALID_REQUEST", "invalid next cursor", 400)
	}
	var next *wire.BytesValue
	if p.Next != "" {
		b, err := base64.RawURLEncoding.Strict().DecodeString(p.Next)
		if err != nil || len(b) > 4096 {
			return nil, boundedError("INVALID_REQUEST", "next must be base64url without padding", 400)
		}
		next = &wire.BytesValue{Value: b}
	}
	peer, err := c.verifiedAccountPeer(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	mid := &wire.AccountMagazineMessage{Peer: peer, Rid: rid, Date: date}
	var req proto.Message
	method := "GetMessageUpvoters"
	if read {
		req = &wire.AccountUpvotersRequest{Message: mid, Next: next}
	} else {
		method = "UpvotePost"
		if op == "message.upvote.remove" {
			method = "RevokeUpvotedPost"
		}
		req = &wire.AccountUpvoteRequest{Message: mid, AlbumId: album}
	}
	var data []byte
	if read {
		data, err = c.readRPC(ctx, "bale.magazine.v1.Magazine", method, req)
	} else {
		data, err = c.rpc(ctx, "bale.magazine.v1.Magazine", method, req)
	}
	if err != nil {
		return nil, err
	}
	if read {
		r := &wire.AccountUpvotersResponse{}
		if decode(data, r) != nil || len(r.Users) > 4096 || len(r.GetNext().GetValue()) > 4096 {
			return nil, protocolError()
		}
		users := []domains.Peer{}
		for _, v := range r.Users {
			if v == nil || v.Id == 0 {
				return nil, protocolError()
			}
			users = append(users, domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(v.Id), 10)})
		}
		for _, v := range r.Users {
			c.rememberRef("user", v)
		}
		return json.Marshal(map[string]any{"users": users, "next": base64.RawURLEncoding.EncodeToString(r.GetNext().GetValue())})
	}
	r := &wire.AccountUpvoteResponse{}
	if decode(data, r) != nil || r.Upvotes == nil || len(r.Upvotes.Messages) > 4096 || r.Upvotes.Limit < 0 {
		return nil, ambiguous()
	}
	for _, v := range r.Upvotes.Messages {
		if v == nil || v.Peer == nil || v.Peer.Id == 0 || v.Rid == 0 || v.Date < 0 {
			return nil, ambiguous()
		}
	}
	// Quota state can reference channels for which the provider supplies only an
	// InPeer. Do not invent public group/channel labels or expose access hashes.
	return json.Marshal(map[string]any{"acknowledged": true, "upvotes_limit": r.Upvotes.Limit, "upvoted_count": len(r.Upvotes.Messages)})
}
