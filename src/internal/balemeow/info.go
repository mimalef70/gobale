package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"strconv"
	"time"
)

func (c *Client) infoCall(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	if op == "account.info" {
		c.mu.Lock()
		session := c.session
		if session == nil {
			c.mu.Unlock()
			return nil, boundedError("AUTH_REQUIRED", "authenticate the account first", 401)
		}
		id, phone := session.UserID, session.Phone
		hash := c.peerHashes["user:"+id]
		c.mu.Unlock()
		numeric, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return nil, protocolError()
		}
		data, err := c.readRPC(ctx, "bale.users.v1.Users", "GetFullUser", &wire.GetUserInfoRequest{Peer: &wire.PeerRef{Id: uint32(numeric), AccessHash: hash}})
		if err != nil {
			return nil, err
		}
		response := &wire.GetUserInfoResponse{}
		if decode(data, response) != nil || response.User == nil || response.User.Id != uint32(numeric) {
			return nil, protocolError()
		}
		u := response.User
		if len(u.Name)+len(u.GetNick().GetValue())+len(u.GetAbout().GetValue()) > 16384 {
			return nil, protocolError()
		}
		c.rememberRef("user", &wire.PeerRef{Id: u.Id, AccessHash: u.AccessHash})
		return json.Marshal(map[string]any{"account_id": id, "name": u.Name, "username": u.GetNick().GetValue(), "about": u.GetAbout().GetValue(), "phone": phone, "is_bot": u.GetIsBot().GetValue(), "is_deleted": u.GetIsDeleted().GetValue()})
	}
	var p struct {
		Peer domains.Peer `json:"peer"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid group info request", 400)
	}
	ref, err := c.groupRef(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	method := "GetFullGroup"
	if op == "group.link" {
		method = "GetGroupInviteURL"
	}
	data, err := c.readRPC(ctx, groupsService, method, &wire.GetGroupInfoRequest{Peer: ref})
	if err != nil {
		return nil, err
	}
	if op == "group.link" {
		response := &wire.GroupInviteURLResponse{}
		if decode(data, response) != nil || response.Url == "" || len(response.Url) > 4096 {
			return nil, protocolError()
		}
		return json.Marshal(map[string]any{"invite_link": response.Url})
	}
	response := &wire.GetGroupInfoResponse{}
	if decode(data, response) != nil || response.Group == nil || response.Group.Id != ref.Id {
		return nil, protocolError()
	}
	g := response.Group
	if len(g.Title)+len(g.GetAbout().GetValue())+len(g.GetNick().GetValue()) > 16384 || g.CreateDate < 0 || g.GetMembersCount().GetValue() < 0 {
		return nil, protocolError()
	}
	peer, err := c.rememberGroupKind(&wire.PeerRef{Id: g.Id, AccessHash: g.AccessHash}, g.GroupType)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"peer": peer, "title": g.Title, "description": g.GetAbout().GetValue(), "username": g.GetNick().GetValue(), "owner_id": strconv.FormatUint(uint64(g.OwnerUid), 10), "created_at": time.UnixMilli(g.CreateDate).UTC(), "members_count": g.GetMembersCount().GetValue(), "is_member": g.GetIsMember().GetValue(), "group_type": g.GroupType})
}
