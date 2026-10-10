package balemeow

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
)

// messageMutation implements the reviewed official messaging subset. One
// source message per forward keeps one journal RID equal to one new message.
func (c *Client) messageMutation(ctx context.Context, operation string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Peer       domains.Peer `json:"peer"`
		SourcePeer domains.Peer `json:"source_peer"`
		MessageID  string       `json:"message_id"`
		SourceDate string       `json:"source_date"`
		Date       string       `json:"date"`
		HideSender bool         `json:"hide_sender"`
		JustMine   *bool        `json:"just_mine"`
		RequestID  string       `json:"request_id"`
	}
	if len(raw) > 128<<10 || json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid message mutation body", 400)
	}
	rid, err := positiveID(p.RequestID)
	if err != nil {
		return nil, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before message mutation", 400)
	}
	messageRID, err := messageID(p.MessageID)
	if err != nil {
		return nil, boundedError("INVALID_REQUEST", "message_id must be a nonzero signed int64", 400)
	}
	if operation == "message.delete" && p.JustMine == nil {
		return nil, boundedError("INVALID_REQUEST", "just_mine must explicitly select own-view or everyone deletion", 400)
	}
	var sourceDate, date int64
	if p.SourceDate != "" {
		sourceDate, err = positiveID(p.SourceDate)
		if err != nil {
			return nil, boundedError("INVALID_REQUEST", "source_date must be a positive decimal millisecond timestamp", 400)
		}
	}
	if p.Date != "" {
		date, err = positiveID(p.Date)
		if err != nil {
			return nil, boundedError("INVALID_REQUEST", "date must be a positive decimal millisecond timestamp", 400)
		}
	}
	if operation == "message.forward" {
		if sourceDate == 0 {
			return nil, boundedError("INVALID_REQUEST", "source_date is required as the original provider timestamp in decimal milliseconds", 400)
		}
		if _, err := encodePeer(p.SourcePeer); err != nil {
			return nil, err
		}
	}
	peer, err := c.messagePeer(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	switch operation {
	case "message.forward":
		source, err := c.messagePeer(ctx, p.SourcePeer)
		if err != nil {
			return nil, err
		}
		reference := &wire.MessageReference{Peer: source, Rid: messageRID}
		if sourceDate > 0 {
			reference.Date = &wire.Int64Value{Value: sourceDate}
		}
		data, err := c.rpc(ctx, "bale.messaging.v2.Messaging", "ForwardMessages", &wire.ForwardMessagesRequest{Peer: peer, Rids: []int64{rid}, ForwardedMessages: []*wire.MessageReference{reference}, HideSender: p.HideSender})
		if err != nil {
			return nil, err
		}
		response := &wire.SendMessageResponse{}
		if decode(data, response) != nil || response.Date <= 0 {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"message_id": strconv.FormatInt(rid, 10), "date": time.UnixMilli(response.Date).UTC(), "acknowledged": true})
	case "message.delete":
		request := &wire.DeleteMessageRequest{Peer: peer, Rids: []int64{messageRID}, JustMine: &wire.BoolValue{Value: *p.JustMine}}
		if date > 0 {
			request.Dates = &wire.MessageDates{Dates: []int64{date}}
		}
		data, err := c.rpc(ctx, "bale.messaging.v2.Messaging", "DeleteMessage", request)
		if err != nil {
			return nil, err
		}
		if decode(data, &wire.Empty{}) != nil {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"message_id": p.MessageID, "just_mine": *p.JustMine, "acknowledged": true})
	default:
		return nil, domains.Unsupported(operation)
	}
}
func (c *Client) messagePeer(ctx context.Context, peer domains.Peer) (*wire.Peer, error) {
	value, err := encodePeer(peer)
	if err != nil {
		return nil, err
	}
	if peer.Type == "user" {
		c.mu.Lock()
		hash, ok := c.peerHashes[peer.Key()]
		c.mu.Unlock()
		if ok {
			value.AccessHash = hash
		}
	}
	if peer.Type == "group" || peer.Type == "channel" {
		ref, err := c.groupRef(ctx, peer)
		if err != nil {
			return nil, err
		}
		value.AccessHash = ref.AccessHash
	}
	return value, nil
}

func (c *Client) exMessagePeer(ctx context.Context, peer domains.Peer) (*wire.Peer, error) {
	value, err := c.messagePeer(ctx, peer)
	if err != nil {
		return nil, err
	}
	return extendedPeer(value, peer), nil
}
