package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"unicode/utf8"
)

func (c *Client) groupMutation(ctx context.Context, operation string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Peer        domains.Peer `json:"peer"`
		User        domains.Peer `json:"user"`
		Description *string      `json:"description"`
		RequestID   string       `json:"request_id"`
	}
	if len(raw) > 64<<10 || json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid group mutation body", 400)
	}
	rid, err := positiveID(p.RequestID)
	if err != nil {
		return nil, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before group mutation", 400)
	}
	if p.Peer.Type != "group" && p.Peer.Type != "channel" {
		return nil, boundedError("INVALID_PEER", "a group or channel peer is required", 400)
	}
	if _, err := encodePeer(p.Peer); err != nil {
		return nil, err
	}
	switch operation {
	case "group.description":
		if p.Description == nil || !utf8.ValidString(*p.Description) || utf8.RuneCountInString(*p.Description) > 4096 {
			return nil, boundedError("INVALID_REQUEST", "description must be present and at most 4096 characters (empty clears it)", 400)
		}
	case "group.remove":
		if p.User.Type != "user" {
			return nil, boundedError("INVALID_PEER", "user must identify a user peer", 400)
		}
		if _, err := encodePeer(p.User); err != nil {
			return nil, err
		}
	default:
		return nil, domains.Unsupported(operation)
	}
	group, err := c.groupRef(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	var method string
	var request proto.Message
	if operation == "group.description" {
		method = "EditGroupAbout"
		// Presence matters: the empty wrapper explicitly clears an existing about.
		request = &wire.GroupDescriptionRequest{Group: group, Rid: rid, About: &wire.StringValue{Value: *p.Description}}
	} else {
		user, err := c.resolvedUserRef(ctx, p.User)
		if err != nil {
			return nil, err
		}
		method = "KickUser"
		request = &wire.GroupRemoveRequest{Group: group, User: user, Rid: rid}
	}
	data, err := c.rpc(ctx, groupsService, method, request)
	if err != nil {
		return nil, err
	}
	if decode(data, &wire.Empty{}) != nil {
		return nil, ambiguous()
	}
	return json.RawMessage(`{"acknowledged":true}`), nil
}
