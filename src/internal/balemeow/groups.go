package balemeow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const groupsService = "bale.groups.v1.Groups"

// Provider access hashes stay inside this per-account cache. They are not
// identifiers and do not appear in public group results.
func (c *Client) rememberRef(kind string, ref *wire.PeerRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := kind + ":" + strconv.FormatUint(uint64(ref.Id), 10)
	if _, exists := c.peerHashes[key]; !exists && len(c.peerHashes) >= 16384 {
		for old := range c.peerHashes {
			delete(c.peerHashes, old)
			break
		}
	}
	c.peerHashes[key] = ref.AccessHash
}
func (c *Client) groupRef(ctx context.Context, peer domains.Peer) (*wire.PeerRef, error) {
	p, err := encodePeer(peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != "group" && peer.Type != "channel" {
		return nil, boundedError("INVALID_PEER", "a group or channel peer is required", 400)
	}
	c.mu.Lock()
	hash, ok := c.peerHashes[peer.Key()]
	c.mu.Unlock()
	if !ok && peer.AccessHash == "" {
		kind := "group"
		if peer.Type == "channel" {
			kind = "channel"
		}
		body, _ := json.Marshal(map[string]string{"kind": kind})
		if _, err := c.groupCall(ctx, "group.list", body); err != nil {
			return nil, err
		}
		c.mu.Lock()
		hash, ok = c.peerHashes[peer.Key()]
		c.mu.Unlock()
		if !ok {
			return nil, boundedError("PEER_NOT_FOUND", "group was not found in the authenticated account", 404)
		}
	}
	if peer.AccessHash != "" {
		hash = p.AccessHash
	}
	return &wire.PeerRef{Id: p.Id, AccessHash: hash}, nil
}
func (c *Client) groupCall(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Peer      domains.Peer   `json:"peer"`
		Users     []domains.Peer `json:"users"`
		Limit     int            `json:"limit"`
		Next      string         `json:"next"`
		Title     string         `json:"title"`
		RequestID string         `json:"request_id"`
		IsOwner   bool           `json:"is_owner"`
		Kind      string         `json:"kind"`
		Username  *string        `json:"username"`
	}
	if len(raw) != 0 && json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid group request", 400)
	}
	if op == "group.list" || op == "channel.list" {
		if op == "channel.list" {
			p.Kind = "channel"
		}
		if p.Kind == "" {
			p.Kind = "group"
		}
		if p.Kind == "all" {
			items := []domains.Peer{}
			for _, kind := range []string{"group", "channel"} {
				body, _ := json.Marshal(map[string]any{"kind": kind, "is_owner": p.IsOwner})
				data, err := c.groupCall(ctx, "group.list", body)
				if err != nil {
					return nil, err
				}
				var reply struct {
					Groups []domains.Peer `json:"groups"`
				}
				if json.Unmarshal(data, &reply) != nil {
					return nil, protocolError()
				}
				items = append(items, reply.Groups...)
			}
			return json.Marshal(map[string]any{"groups": items})
		}
		mode, groupKind := int32(1), int32(0)
		if p.Kind == "channel" {
			mode, groupKind = 2, 1
		} else if p.Kind != "group" {
			return nil, groupRequestError("kind must be group, channel or all")
		}
		data, err := c.readRPC(ctx, groupsService, "GetMyGroups", &wire.GetMyGroupsRequest{Mode: mode, IsOwner: p.IsOwner})
		if err != nil {
			return nil, err
		}
		response := &wire.GetMyGroupsResponse{}
		if decode(data, response) != nil || len(response.Groups) > 4096 {
			return nil, protocolError()
		}
		out := []domains.Peer{}
		for _, r := range response.Groups {
			if r.Id == 0 {
				return nil, protocolError()
			}
			peer, err := c.rememberGroupKind(r, groupKind)
			if err != nil {
				return nil, err
			}
			out = append(out, peer)
		}
		return json.Marshal(map[string]any{"groups": out})
	}
	if op == "group.members" {
		ref, err := c.groupRef(ctx, p.Peer)
		if err != nil {
			return nil, err
		}
		limit, _, err := pageArgs(p.Limit, "")
		if err != nil {
			return nil, err
		}
		cursor, err := base64.RawURLEncoding.DecodeString(p.Next)
		if err != nil || len(cursor) > 4096 {
			return nil, boundedError("INVALID_REQUEST", "invalid group member cursor", 400)
		}
		request := &wire.GroupMembersRequest{Group: ref, Limit: limit}
		if len(cursor) > 0 {
			request.Next = &wire.BytesValue{Value: cursor}
		}
		data, err := c.readRPC(ctx, groupsService, "LoadMembers", request)
		if err != nil {
			return nil, err
		}
		response := &wire.GroupMembersResponse{}
		if decode(data, response) != nil || len(response.Members) > int(limit) || len(response.GetNext().GetValue()) > 4096 {
			return nil, protocolError()
		}
		members := []map[string]any{}
		for _, m := range response.Members {
			if m.Uid == 0 || m.Date < 0 {
				return nil, protocolError()
			}
			members = append(members, map[string]any{"user_id": strconv.FormatUint(uint64(m.Uid), 10), "inviter_id": strconv.FormatUint(uint64(m.InviterUid), 10), "joined_at": time.UnixMilli(m.Date).UTC(), "is_admin": m.GetIsAdmin().GetValue()})
		}
		return json.Marshal(map[string]any{"members": members, "next": base64.RawURLEncoding.EncodeToString(response.GetNext().GetValue())})
	}
	rid, err := positiveID(p.RequestID)
	if err != nil {
		return nil, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before group mutation", 400)
	}
	if op == "channel.create" {
		op = "group.create"
		p.Kind = "channel"
	}
	if op == "group.create" || op == "group.title" {
		p.Title = strings.TrimSpace(p.Title)
		if p.Title == "" || !utf8.ValidString(p.Title) || utf8.RuneCountInString(p.Title) > 255 {
			return nil, boundedError("INVALID_REQUEST", "group title must contain 1 to 255 characters", 400)
		}
	}
	if op == "group.create" {
		if p.Kind != "" && p.Kind != "group" && p.Kind != "channel" && p.Kind != "supergroup" {
			return nil, groupRequestError("kind must be group, channel or supergroup")
		}
		if p.Username != nil && !validGroupUsername(*p.Username, false) {
			return nil, groupRequestError("username must contain 1 to 64 ASCII letters, digits or underscores")
		}
	}
	users := []*wire.PeerRef{}
	if op == "group.create" || op == "group.invite" {
		if len(p.Users) > 100 || (op == "group.invite" && len(p.Users) == 0) {
			return nil, boundedError("INVALID_REQUEST", "provide at most 100 users (at least one for invite)", 400)
		}
		seen := map[string]bool{}
		for _, user := range p.Users {
			if user.Type != "user" || seen[user.ID] {
				return nil, boundedError("INVALID_PEER", "provide unique user peers", 400)
			}
			seen[user.ID] = true
			ref, err := c.resolvedUserRef(ctx, user)
			if err != nil {
				return nil, err
			}
			users = append(users, ref)
		}
	}
	if op == "group.create" {
		q := &wire.GroupCreateExtendedRequest{Rid: rid, Title: p.Title, Users: users}
		if p.Kind == "channel" {
			q.GroupType = 1
		} else if p.Kind == "supergroup" {
			q.GroupType = 2
		}
		if p.Username != nil {
			q.Nick = &wire.StringValue{Value: *p.Username}
			q.Restriction = 1
		}
		data, err := c.rpc(ctx, groupsService, "CreateGroup", q)
		if err != nil {
			return nil, err
		}
		response := &wire.GroupCreateResponse{}
		if decode(data, response) != nil || response.Group == nil || response.Group.Id == 0 || !validNotAdded(response.NotAdded, users) {
			return nil, ambiguous()
		}
		g := response.Group
		if len(g.Title) > 4096 || len(response.InviteLink) > 4096 {
			return nil, ambiguous()
		}
		if g.GroupType != q.GroupType {
			return nil, ambiguous()
		}
		peer, err := c.rememberGroupKind(&wire.PeerRef{Id: g.Id, AccessHash: g.AccessHash}, g.GroupType)
		if err != nil {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"peer": peer, "title": g.Title, "invite_link": response.InviteLink, "not_added_user_ids": refIDs(response.NotAdded), "group_type": g.GroupType})
	}
	ref, err := c.groupRef(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	if op == "group.invite" {
		data, err := c.rpc(ctx, groupsService, "InviteUsers", &wire.GroupInviteRequest{Group: ref, Rid: rid, Users: users})
		if err != nil {
			return nil, err
		}
		response := &wire.GroupInviteResponse{}
		if decode(data, response) != nil || !validNotAdded(response.NotAdded, users) {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "not_added_user_ids": refIDs(response.NotAdded)})
	}
	data, err := c.rpc(ctx, groupsService, "EditGroupTitle", &wire.GroupTitleRequest{Group: ref, Rid: rid, Title: p.Title})
	if err != nil {
		return nil, err
	}
	if decode(data, &wire.SendMessageResponse{}) != nil {
		return nil, ambiguous()
	}
	return json.RawMessage(`{"acknowledged":true}`), nil
}
func refIDs(refs []*wire.PeerRef) []string {
	ids := []string{}
	for _, r := range refs {
		ids = append(ids, strconv.FormatUint(uint64(r.Id), 10))
	}
	return ids
}

func validNotAdded(refs, users []*wire.PeerRef) bool {
	if len(refs) > len(users) {
		return false
	}
	allowed := map[uint32]bool{}
	for _, u := range users {
		allowed[u.Id] = true
	}
	seen := map[uint32]bool{}
	for _, r := range refs {
		if r.Id == 0 || !allowed[r.Id] || seen[r.Id] {
			return false
		}
		seen[r.Id] = true
	}
	return true
}
