package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"strconv"
	"strings"
	"unicode/utf8"
)

type reactionMessage struct {
	MessageID string `json:"message_id"`
	DateMS    string `json:"date_ms"`
	Sequence  string `json:"sequence"`
}
type presenceReactionRequest struct {
	Peer       domains.Peer      `json:"peer"`
	Users      []domains.Peer    `json:"users"`
	Online     *bool             `json:"online"`
	TimeoutMS  int64             `json:"timeout_ms"`
	TypingType int32             `json:"typing_type"`
	MessageID  string            `json:"message_id"`
	DateMS     string            `json:"date_ms"`
	Messages   []reactionMessage `json:"messages"`
	Code       string            `json:"code"`
	Page       int32             `json:"page"`
	Limit      int32             `json:"limit"`
	RequestID  string            `json:"request_id"`
}

func accountDate(value string) (int64, error) {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, boundedError("INVALID_REQUEST", "date_ms must be a nonnegative int64 decimal string", 400)
	}
	n, e := strconv.ParseInt(value, 10, 64)
	if e != nil {
		return 0, boundedError("INVALID_REQUEST", "date_ms is outside int64 range", 400)
	}
	return n, nil
}
func reactionMessageIDs(items []reactionMessage) ([]*wire.ReactionMessageID, error) {
	if len(items) < 1 || len(items) > 100 {
		return nil, boundedError("INVALID_REQUEST", "messages must contain 1 to 100 items", 400)
	}
	out := make([]*wire.ReactionMessageID, 0, len(items))
	seen := map[int64]bool{}
	for _, v := range items {
		rid, err := messageID(v.MessageID)
		if err != nil || seen[rid] {
			return nil, boundedError("INVALID_REQUEST", "message IDs must be unique nonzero signed int64 strings", 400)
		}
		seen[rid] = true
		date, err := accountDate(v.DateMS)
		if err != nil {
			return nil, err
		}
		sequence := int64(0)
		if v.Sequence != "" {
			sequence, err = accountDate(v.Sequence)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, &wire.ReactionMessageID{Rid: rid, Date: date, Sequence: sequence})
	}
	return out, nil
}
func (c *Client) verifiedAccountPeer(ctx context.Context, peer domains.Peer) (*wire.Peer, error) {
	if peer.AccessHash != "" {
		return nil, boundedError("INVALID_PEER", "caller-supplied access hashes are not accepted", 400)
	}
	v, err := encodePeer(peer)
	if err != nil {
		return nil, err
	}
	if peer.Type == "user" {
		ref, err := c.accountUserRef(ctx, peer)
		if err != nil {
			return nil, err
		}
		v.AccessHash = ref.AccessHash
		return v, nil
	}
	ref, err := c.groupRef(ctx, peer)
	if err != nil {
		return nil, err
	}
	v.AccessHash = ref.AccessHash
	return v, nil
}
func reactionSummaries(values []*wire.ReactionSummary) ([]map[string]any, error) {
	if len(values) > 128 {
		return nil, protocolError()
	}
	out := []map[string]any{}
	for _, r := range values {
		if r == nil || r.Code == "" || !utf8.ValidString(r.Code) || len(r.Code) > 128 || len(r.Users) > 4096 || r.GetCount().GetValue() < 0 {
			return nil, protocolError()
		}
		users := make([]string, 0, len(r.Users))
		for _, u := range r.Users {
			if u == 0 {
				return nil, protocolError()
			}
			users = append(users, strconv.FormatUint(uint64(u), 10))
		}
		v := map[string]any{"code": r.Code, "user_ids": users}
		if r.Count != nil {
			v["count"] = strconv.FormatInt(r.Count.Value, 10)
		}
		out = append(out, v)
	}
	return out, nil
}
func (c *Client) presenceReactionCall(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	var p presenceReactionRequest
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > 128<<10 || decodeAccountBody(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid presence or reaction request", 400)
	}
	read := true
	ephemeral := false
	switch op {
	case "message.reaction.set", "message.reaction.remove", "message.views.increment":
		read = false
		if err := requireAccountMutationID(p.RequestID); err != nil {
			return nil, err
		}
	case "presence.online", "presence.typing", "presence.stop":
		read = false
		ephemeral = true
	}
	service, method := "bale.presence.v1.Presence", ""
	var req proto.Message
	var mids []*wire.ReactionMessageID
	var err error
	bad := func() (json.RawMessage, error) {
		return nil, boundedError("INVALID_REQUEST", "invalid or missing presence/reaction field", 400)
	}
	switch op {
	case "presence.online":
		if p.Online == nil || p.TimeoutMS < 0 || p.TimeoutMS > 90000 {
			return bad()
		}
		if p.TimeoutMS == 0 {
			p.TimeoutMS = 90000
		}
		method = "SetOnline"
		req = &wire.PresenceOnlineRequest{IsOnline: *p.Online, Timeout: p.TimeoutMS, DeviceType: 2}
	case "presence.typing", "presence.stop":
		if p.TypingType == 0 {
			p.TypingType = 1
		}
		if p.TypingType < 1 || p.TypingType > 12 {
			return bad()
		}
		peer, e := c.verifiedAccountPeer(ctx, p.Peer)
		if e != nil {
			return nil, e
		}
		if op == "presence.typing" {
			method = "Typing"
			req = &wire.PresenceTypingRequest{Peer: peer, TypingType: p.TypingType}
		} else {
			method = "StopTyping"
			req = &wire.PresenceStopTypingRequest{Peer: peer, TypingType: p.TypingType}
		}
	case "presence.users":
		if len(p.Users) < 1 || len(p.Users) > 100 {
			return bad()
		}
		ids := []uint32{}
		seen := map[string]bool{}
		for _, user := range p.Users {
			if user.Type != "user" || user.AccessHash != "" || seen[user.ID] {
				return bad()
			}
			if _, e := encodePeer(user); e != nil {
				return nil, e
			}
			seen[user.ID] = true
		}
		for _, user := range p.Users {
			ref, e := c.accountUserRef(ctx, user)
			if e != nil {
				return nil, e
			}
			ids = append(ids, ref.Id)
		}
		method = "GetUsersPresence"
		req = &wire.PresenceUsersRequest{UserIds: ids}
	case "presence.contacts":
		if p.Limit == 0 {
			p.Limit = 100
		}
		if p.Limit < 1 || p.Limit > 100 {
			return bad()
		}
		method = "GetContactsPresences"
		req = &wire.PresenceContactsRequest{Limit: &wire.Int32Value{Value: p.Limit}}
	case "presence.group", "presence.group.count":
		if p.Peer.Type != "group" && p.Peer.Type != "channel" {
			return bad()
		}
		peer, e := c.verifiedAccountPeer(ctx, p.Peer)
		if e != nil {
			return nil, e
		}
		method = "GetGroupMembersPresences"
		if op == "presence.group.count" {
			method = "GetGroupOnlineCount"
		}
		req = &wire.AccountPeerRequest{Peer: &wire.PeerRef{Id: peer.Id, AccessHash: peer.AccessHash}}
	case "message.reaction.set", "message.reaction.remove", "message.reaction.users":
		rid, e := messageID(p.MessageID)
		if e != nil {
			return bad()
		}
		date, e := accountDate(p.DateMS)
		if e != nil {
			return nil, e
		}
		if !utf8.ValidString(p.Code) || utf8.RuneCountInString(p.Code) > 32 || (op != "message.reaction.users" && p.Code == "") {
			return bad()
		}
		if p.Page < 0 || p.Page > 100000 || p.Limit < 0 || p.Limit > 100 {
			return bad()
		}
		if p.Limit == 0 {
			p.Limit = 20
		}
		peer, e := c.verifiedAccountPeer(ctx, p.Peer)
		if e != nil {
			return nil, e
		}
		service = "bale.abacus.v1.Abacus"
		if op == "message.reaction.users" {
			method = "GetMessageReactionsList"
			req = &wire.ReactionUsersRequest{Peer: peer, Rid: rid, Date: date, Code: p.Code, Page: p.Page, Limit: p.Limit}
		} else {
			method = "MessageSetReaction"
			if op == "message.reaction.remove" {
				method = "MessageRemoveReaction"
			}
			req = &wire.ReactionMutationRequest{Peer: peer, Rid: rid, Date: date, Code: p.Code}
		}
	case "message.reactions", "message.views", "message.views.increment":
		mids, err = reactionMessageIDs(p.Messages)
		if err != nil {
			return nil, err
		}
		peer, e := c.verifiedAccountPeer(ctx, p.Peer)
		if e != nil {
			return nil, e
		}
		service = "bale.abacus.v1.Abacus"
		if op == "message.reactions" {
			method = "GetMessagesReactions"
			req = &wire.ReactionsRequest{Peer: peer, Mids: mids}
		} else {
			method = "GetMessagesViews"
			req = &wire.ViewsRequest{Peer: peer, Mids: mids, Increment: op == "message.views.increment"}
		}
	default:
		return nil, domains.Unsupported(op)
	}
	var data []byte
	if read {
		data, err = c.readRPC(ctx, service, method, req)
	} else {
		data, err = c.rpc(ctx, service, method, req)
	}
	if err != nil {
		return nil, err
	}
	malformed := func() (json.RawMessage, error) {
		if read {
			return nil, protocolError()
		}
		return nil, ambiguous()
	}
	if ephemeral {
		if decode(data, &wire.Empty{}) != nil {
			return malformed()
		}
		return json.RawMessage(`{"acknowledged":true,"ephemeral":true}`), nil
	}
	switch op {
	case "presence.group.count":
		r := &wire.PresenceCountResponse{}
		if decode(data, r) != nil || r.Count < 0 {
			return malformed()
		}
		return json.Marshal(map[string]any{"peer": p.Peer, "count": r.Count})
	case "presence.users", "presence.contacts", "presence.group":
		r := &wire.PresenceResponse{}
		max := 4096
		if op == "presence.users" {
			max = len(p.Users)
		}
		if op == "presence.contacts" {
			max = int(p.Limit)
		}
		if decode(data, r) != nil || len(r.Presences) > max {
			return malformed()
		}
		out := []map[string]any{}
		allowed := map[uint32]bool{}
		for _, v := range p.Users {
			id, _ := strconv.ParseUint(v.ID, 10, 32)
			allowed[uint32(id)] = true
		}
		for _, v := range r.Presences {
			if v == nil || (v.Online == nil) == (v.Offline == nil) {
				return malformed()
			}
			var ref *wire.PeerRef
			item := map[string]any{"online": v.Online != nil}
			if v.Online != nil {
				ref = v.Online.Peer
				if v.Online.GetExpiresAt().GetValue() < 0 {
					return malformed()
				}
				if v.Online.ExpiresAt != nil {
					item["expires_at_ms"] = strconv.FormatInt(v.Online.ExpiresAt.Value, 10)
				}
			} else {
				ref = v.Offline.Peer
				if v.Offline.GetDate().GetValue() < 0 || v.Offline.UnknownLastSeenValue < 0 || v.Offline.UnknownLastSeenValue > 3 {
					return malformed()
				}
				item["last_seen_unknown"] = v.Offline.GetLastSeenUnknown().GetValue()
				item["last_seen_bucket"] = v.Offline.UnknownLastSeenValue
				if v.Offline.Date != nil {
					item["last_seen_ms"] = strconv.FormatInt(v.Offline.Date.Value, 10)
				}
			}
			if ref == nil || ref.Id == 0 || (op == "presence.users" && !allowed[ref.Id]) {
				return malformed()
			}
			item["peer"] = domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(ref.Id), 10)}
			out = append(out, item)
		}
		return json.Marshal(map[string]any{"presences": out})
	case "message.reaction.set", "message.reaction.remove":
		r := &wire.ReactionMutationResponse{}
		if decode(data, r) != nil {
			return malformed()
		}
		values, e := reactionSummaries(r.Reactions)
		if e != nil {
			return malformed()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "message_id": p.MessageID, "reactions": values})
	case "message.reaction.users":
		r := &wire.ReactionUsersResponse{}
		if decode(data, r) != nil || len(r.Users) > int(p.Limit) {
			return malformed()
		}
		out := []map[string]any{}
		for _, v := range r.Users {
			if v == nil || v.UserId == 0 || len(v.Code) > 128 || v.ReactionTime < 0 {
				return malformed()
			}
			out = append(out, map[string]any{"user_id": strconv.FormatUint(uint64(v.UserId), 10), "code": v.Code, "date_ms": strconv.FormatInt(v.ReactionTime, 10)})
		}
		return json.Marshal(map[string]any{"users": out, "page": p.Page, "limit": p.Limit})
	case "message.reactions":
		r := &wire.ReactionsResponse{}
		if decode(data, r) != nil || len(r.Containers) > len(mids) {
			return malformed()
		}
		allowed := map[int64]bool{}
		for _, v := range mids {
			allowed[v.Rid] = true
		}
		out := []map[string]any{}
		for _, v := range r.Containers {
			if v == nil || !allowed[v.Rid] || v.Date < 0 {
				return malformed()
			}
			values, e := reactionSummaries(v.Reactions)
			if e != nil {
				return malformed()
			}
			out = append(out, map[string]any{"message_id": strconv.FormatInt(v.Rid, 10), "date_ms": strconv.FormatInt(v.Date, 10), "reactions": values})
		}
		return json.Marshal(map[string]any{"messages": out})
	case "message.views", "message.views.increment":
		r := &wire.ViewsResponse{}
		if decode(data, r) != nil || len(r.Containers) > len(mids) {
			return malformed()
		}
		allowed := map[int64]bool{}
		for _, v := range mids {
			allowed[v.Rid] = true
		}
		out := []map[string]any{}
		for _, v := range r.Containers {
			if v == nil || v.Mid == nil || !allowed[v.Mid.Rid] || v.Mid.Date < 0 || v.Mid.Sequence < 0 || v.GetViews().GetValue() < 0 {
				return malformed()
			}
			item := map[string]any{"message_id": strconv.FormatInt(v.Mid.Rid, 10), "date_ms": strconv.FormatInt(v.Mid.Date, 10)}
			if v.Views != nil {
				item["views"] = strconv.FormatInt(v.Views.Value, 10)
			}
			out = append(out, item)
		}
		return json.Marshal(map[string]any{"messages": out, "incremented": !read})
	}
	return nil, domains.Unsupported(op)
}
