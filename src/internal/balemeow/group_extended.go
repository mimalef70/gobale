package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var permissionNames = []string{
	"see_message", "delete_message", "kick_user", "pin_message", "invite_user",
	"add_admin", "change_info", "send_message", "see_members", "edit_message",
	"send_media", "send_gif_stickers", "reply_to_story", "forward_message_from",
	"send_gift_packet", "start_call", "send_link_message", "send_forwarded_message",
	"add_story", "manage_call",
}

var groupReadOperations = map[string]bool{
	"group.permissions": true, "group.default_permissions": true,
	"group.admins": true, "group.banned": true, "group.preview": true, "group.pins": true,
}

type groupExtendedPayload struct {
	Peer           domains.Peer     `json:"peer"`
	User           domains.Peer     `json:"user"`
	RequestID      string           `json:"request_id"`
	AdminTitle     *string          `json:"admin_title"`
	Permissions    map[string]*bool `json:"permissions"`
	Mode           string           `json:"mode"`
	Token          string           `json:"token"`
	Username       *string          `json:"username"`
	Restriction    string           `json:"restriction"`
	Show           *bool            `json:"show"`
	CanSeeMessages *bool            `json:"can_see_messages"`
	MakeOrphan     *bool            `json:"make_orphan"`
	SenderID       string           `json:"sender_id"`
	MessageID      string           `json:"message_id"`
	Date           string           `json:"date"`
	Page           int              `json:"page"`
	Limit          int              `json:"limit"`
	AvatarID       string           `json:"avatar_id"`
	MediaID        string           `json:"media_id"`
}

func groupRequestError(message string) error {
	return boundedError("INVALID_REQUEST", message, 400)
}

// All durable mutations require a journal RID, including RPCs which do not
// carry a RID on the wire. Such RPCs remain unknown after ambiguous writes.
func (c *Client) groupExtended(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	var p groupExtendedPayload
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > 128<<10 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return nil, groupRequestError("invalid group operation body")
	}
	mutations := map[string]bool{
		"group.promote": true, "group.demote": true, "group.transfer": true,
		"group.permissions.set": true, "group.default_permissions.set": true,
		"group.join": true, "group.join_public": true, "group.leave": true,
		"group.link.revoke": true, "group.username": true, "group.restriction": true,
		"group.history": true, "group.visibility": true, "group.pin": true,
		"group.unpin": true, "group.unpin_all": true, "group.unban": true,
		"group.photo.remove": true, "group.photo": true,
	}
	if !groupReadOperations[op] && !mutations[op] {
		return nil, domains.Unsupported(op)
	}
	var rid, msgRID, date, avatarID int64
	var sender uint64
	var err error
	if mutations[op] {
		rid, err = positiveID(p.RequestID)
		if err != nil {
			return nil, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before group mutation", 400)
		}
	}
	needsUser := op == "group.permissions" || op == "group.permissions.set" || op == "group.promote" || op == "group.demote" || op == "group.transfer" || op == "group.unban" || op == "group.visibility"
	if needsUser {
		if p.User.Type != "user" {
			return nil, boundedError("INVALID_PEER", "user must identify a user peer", 400)
		}
		if _, err = encodePeer(p.User); err != nil {
			return nil, err
		}
	}
	if p.AdminTitle != nil && (!utf8.ValidString(*p.AdminTitle) || utf8.RuneCountInString(*p.AdminTitle) > 128) {
		return nil, groupRequestError("admin_title must contain at most 128 characters")
	}
	if op == "group.permissions.set" || op == "group.default_permissions.set" {
		if err = validatePermissionPatch(p.Permissions, p.Mode); err != nil {
			return nil, err
		}
	}
	if op == "group.history" && p.Show == nil {
		return nil, groupRequestError("show must be explicitly true or false")
	}
	if op == "group.visibility" && p.CanSeeMessages == nil {
		return nil, groupRequestError("can_see_messages must be explicitly true or false")
	}
	if op == "group.username" && (p.Username == nil || !validGroupUsername(*p.Username, true)) {
		return nil, groupRequestError("username must be present and contain ASCII letters, digits or underscores; empty clears it")
	}
	if op == "group.restriction" {
		if p.Restriction != "private" && p.Restriction != "public" {
			return nil, groupRequestError("restriction must be private or public")
		}
		if p.Restriction == "public" && (p.Username == nil || !validGroupUsername(*p.Username, false)) {
			return nil, groupRequestError("a valid nonempty username is required for public groups")
		}
		if p.Restriction == "private" && p.Username != nil {
			return nil, groupRequestError("omit username when making a group private")
		}
	}
	if op == "group.pin" || op == "group.unpin" {
		msgRID, err = messageID(p.MessageID)
		if err != nil {
			return nil, groupRequestError("message_id must be a nonzero signed int64")
		}
		date, err = positiveID(p.Date)
		if err != nil {
			return nil, groupRequestError("date must be the original positive provider timestamp in milliseconds")
		}
	}
	if op == "group.pin" {
		sender, err = strconv.ParseUint(p.SenderID, 10, 32)
		if err != nil || sender == 0 || strconv.FormatUint(sender, 10) != p.SenderID {
			return nil, groupRequestError("sender_id must be the original sender's canonical positive user ID")
		}
	}
	if op == "group.photo.remove" && p.AvatarID != "" {
		avatarID, err = messageID(p.AvatarID)
		if err != nil {
			return nil, groupRequestError("avatar_id must be a nonzero signed int64")
		}
	}
	if op == "group.photo" && (p.MediaID == "" || len(p.MediaID) > 128) {
		return nil, groupRequestError("media_id is required")
	}
	var page, limit int32
	if op == "group.pins" {
		limit, _, err = pageArgs(p.Limit, "")
		if err != nil {
			return nil, err
		}
		if p.Page == 0 {
			p.Page = 1
		}
		if p.Page < 1 || p.Page > 1000000 {
			return nil, groupRequestError("page must be between 1 and 1000000")
		}
		page = int32(p.Page)
	}
	if op == "group.preview" || op == "group.join" {
		token, err := groupInviteToken(p.Token)
		if err != nil {
			return nil, err
		}
		if op == "group.preview" {
			data, err := c.readRPC(ctx, groupsService, "GetGroupPreview", &wire.GroupJoinRequest{Token: token})
			if err != nil {
				return nil, err
			}
			reply := &wire.GroupFullExtendedResponse{}
			if decode(data, reply) != nil || reply.Group == nil {
				return nil, protocolError()
			}
			return c.groupFullJSON(reply.Group, reply.Action)
		}
		data, err := c.rpc(ctx, groupsService, "JoinGroup", &wire.GroupJoinRequest{Token: token})
		return c.groupJoinedJSON(data, err)
	}
	if p.Peer.Type != "group" && p.Peer.Type != "channel" {
		return nil, boundedError("INVALID_PEER", "a group or channel peer is required", 400)
	}
	if _, err := encodePeer(p.Peer); err != nil {
		return nil, err
	}
	if op == "group.join_public" {
		// Public joining only needs InPeer. A contact hash must not be invented.
		peer, _ := encodePeer(p.Peer)
		peer.AccessHash = 0
		data, err := c.rpc(ctx, groupsService, "JoinPublicGroup", &wire.GroupJoinPublicRequest{Peer: peer})
		return c.groupJoinedJSON(data, err)
	}
	group, err := c.groupRef(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	var user *wire.PeerRef
	if needsUser {
		user, err = c.resolvedUserRef(ctx, p.User)
		if err != nil {
			return nil, err
		}
	}
	if op == "group.permissions" || op == "group.default_permissions" || op == "group.permissions.set" || op == "group.default_permissions.set" {
		return c.groupPermissionsCall(ctx, op, group, user, p)
	}
	if op == "group.admins" || op == "group.banned" || op == "group.pins" {
		return c.groupListExtended(ctx, op, group, p.Peer, page, limit)
	}
	var method string
	var request proto.Message
	switch op {
	case "group.promote":
		method = "MakeUserAdmin"
		q := &wire.GroupAdminRequest{Group: group, User: user}
		if p.AdminTitle != nil {
			q.AdminTitle = &wire.StringValue{Value: *p.AdminTitle}
		}
		request = q
	case "group.demote":
		method, request = "RemoveUserAdmin", &wire.GroupAdminRequest{Group: group, User: user}
	case "group.transfer":
		method, request = "TransferOwnership", &wire.GroupTransferRequest{Group: group, NewOwner: user.Id}
	case "group.leave":
		q := &wire.GroupLeaveRequest{Group: group, Rid: rid}
		if p.MakeOrphan != nil {
			q.MakeOrphan = &wire.BoolValue{Value: *p.MakeOrphan}
		}
		method, request = "LeaveGroup", q
	case "group.link.revoke":
		method, request = "RevokeInviteURL", &wire.GetGroupInfoRequest{Peer: group}
	case "group.username":
		method, request = "EditChannelNick", &wire.GroupUsernameRequest{Group: group, Nick: *p.Username, Rid: rid}
	case "group.restriction":
		q := &wire.GroupRestrictionRequest{Group: group}
		if p.Restriction == "public" {
			q.Restriction = 1
			q.Nick = &wire.StringValue{Value: *p.Username}
		}
		method, request = "SetRestriction", q
	case "group.history":
		method, request = "SetCanSeeHistory", &wire.GroupHistoryRequest{Group: group, CanSeeHistory: *p.Show}
	case "group.visibility":
		method, request = "SetCanSeeMessages", &wire.GroupVisibilityRequest{Group: group, UserId: user.Id, CanSeeMessages: *p.CanSeeMessages}
	case "group.pin":
		method, request = "PinMessage", &wire.GroupPinRequest{Group: group, SenderUid: uint32(sender), Date: date, Rid: msgRID}
	case "group.unpin":
		method, request = "RemoveSinglePin", &wire.GroupUnpinRequest{Group: group, Date: date, Rid: msgRID}
	case "group.unpin_all":
		method, request = "RemovePin", &wire.GetGroupInfoRequest{Peer: group}
	case "group.unban":
		method, request = "UnBanUser", &wire.GroupAdminRequest{Group: group, User: user}
	case "group.photo.remove":
		q := &wire.GroupRemovePhotoRequest{Group: group, Rid: rid}
		if avatarID != 0 {
			q.AvatarId = &wire.Int64Value{Value: avatarID}
		}
		method, request = "RemoveGroupAvatar", q
	case "group.photo":
		file, err := c.uploadProfileImage(ctx, p.MediaID)
		if err != nil {
			return nil, err
		}
		method, request = "EditGroupAvatar", &wire.GroupPhotoRequest{Group: group, Rid: rid, FileLocation: file}
	}
	data, err := c.rpc(ctx, groupsService, method, request)
	if err != nil {
		return nil, err
	}
	if op == "group.link.revoke" {
		reply := &wire.GroupInviteURLResponse{}
		if decode(data, reply) != nil || reply.Url == "" || len(reply.Url) > 4096 {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"invite_link": reply.Url, "acknowledged": true})
	}
	if decode(data, &wire.Empty{}) != nil {
		return nil, ambiguous()
	}
	if op == "group.leave" {
		c.forgetGroup(group.Id)
	}
	return json.RawMessage(`{"acknowledged":true}`), nil
}

func validatePermissionPatch(p map[string]*bool, mode string) error {
	if mode != "" && mode != "patch" && mode != "replace" {
		return groupRequestError("mode must be patch or replace")
	}
	if len(p) == 0 {
		return groupRequestError("permissions must contain at least one explicit boolean")
	}
	known := map[string]bool{}
	for _, key := range permissionNames {
		known[key] = true
	}
	for key, value := range p {
		if !known[key] || value == nil {
			return groupRequestError("permissions contains an unknown name or non-boolean value")
		}
	}
	if mode == "replace" && len(p) != len(permissionNames) {
		return groupRequestError("replace requires all 20 permission booleans explicitly")
	}
	return nil
}

func permissionsJSON(p *wire.GroupPermissions) map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	m := p.ProtoReflect()
	for _, name := range permissionNames {
		f := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if f.Kind() == protoreflect.BoolKind {
			out[name] = m.Get(f).Bool()
		} else if m.Has(f) {
			wrapper := m.Get(f).Message()
			out[name] = wrapper.Get(wrapper.Descriptor().Fields().ByNumber(1)).Bool()
		}
	}
	return out
}

func applyPermissions(p *wire.GroupPermissions, patch map[string]*bool) {
	m := p.ProtoReflect()
	for key, value := range patch {
		f := m.Descriptor().Fields().ByName(protoreflect.Name(key))
		if f.Kind() == protoreflect.BoolKind {
			m.Set(f, protoreflect.ValueOfBool(*value))
		} else {
			m.Set(f, protoreflect.ValueOfMessage((&wire.BoolValue{Value: *value}).ProtoReflect()))
		}
	}
}

func (c *Client) groupPermissionsCall(ctx context.Context, op string, group, user *wire.PeerRef, p groupExtendedPayload) (json.RawMessage, error) {
	var permissions *wire.GroupPermissions
	member := op == "group.permissions" || op == "group.permissions.set"
	if member {
		data, err := c.readRPC(ctx, groupsService, "GetMemberPermissions", &wire.GroupMemberPermissionsRequest{Group: group, User: user})
		if err != nil {
			return nil, err
		}
		reply := &wire.GroupMemberPermissionsResponse{}
		if decode(data, reply) != nil || reply.Permissions == nil {
			return nil, protocolError()
		}
		permissions = reply.Permissions
	} else {
		data, err := c.readRPC(ctx, groupsService, "GetFullGroup", &wire.GetGroupInfoRequest{Peer: group})
		if err != nil {
			return nil, err
		}
		reply := &wire.GroupFullExtendedResponse{}
		if decode(data, reply) != nil || reply.Group == nil || reply.Group.Id != group.Id || reply.Group.DefaultPermissions == nil {
			return nil, protocolError()
		}
		permissions = reply.Group.DefaultPermissions
	}
	if groupReadOperations[op] {
		return json.Marshal(map[string]any{"permissions": permissionsJSON(permissions)})
	}
	// Preserve provider fields unknown to this release as well as every omitted
	// known field. A read failure never falls back to an all-false replacement.
	applyPermissions(permissions, p.Permissions)
	method := "SetGroupDefaultPermissions"
	var request proto.Message = &wire.GroupSetDefaultPermissionsRequest{Group: group, Permissions: permissions}
	if member {
		method = "SetMemberPermissions"
		request = &wire.GroupSetMemberPermissionsRequest{Group: group, User: user, Permissions: permissions}
	}
	data, err := c.rpc(ctx, groupsService, method, request)
	if err != nil {
		return nil, err
	}
	if decode(data, &wire.Empty{}) != nil {
		return nil, ambiguous()
	}
	return json.Marshal(map[string]any{"acknowledged": true, "permissions": permissionsJSON(permissions)})
}

func (c *Client) groupListExtended(ctx context.Context, op string, group *wire.PeerRef, peer domains.Peer, page, limit int32) (json.RawMessage, error) {
	if op == "group.pins" {
		data, err := c.readRPC(ctx, groupsService, "GetPins", &wire.GroupPinsRequest{Group: group, Page: page, Limit: limit})
		if err != nil {
			return nil, err
		}
		reply := &wire.GroupPinsResponse{}
		if decode(data, reply) != nil || len(reply.Pins) > int(limit) || reply.Count < 0 {
			return nil, protocolError()
		}
		items := []map[string]any{}
		for _, pin := range reply.Pins {
			if pin == nil || pin.Rid == 0 || pin.Date <= 0 || pin.SenderUid == 0 || validateMessage(pin.Message) != nil {
				return nil, protocolError()
			}
			items = append(items, map[string]any{"message_id": strconv.FormatInt(pin.Rid, 10), "sender_id": strconv.FormatUint(uint64(pin.SenderUid), 10), "date": time.UnixMilli(pin.Date).UTC(), "peer": c.canonicalPeer(peer), "payload": historyPayload(pin.Message, false)})
		}
		return json.Marshal(map[string]any{"pins": items, "count": reply.Count, "page": page})
	}
	method := "GetBannedUsers"
	if op == "group.admins" {
		method = "FetchGroupAdmins"
	}
	data, err := c.readRPC(ctx, groupsService, method, &wire.GetGroupInfoRequest{Peer: group})
	if err != nil {
		return nil, err
	}
	if op == "group.admins" {
		reply := &wire.GroupAdminsResponse{}
		if decode(data, reply) != nil || len(reply.Users)+len(reply.Admins) > 4096 {
			return nil, protocolError()
		}
		for _, user := range reply.Users {
			if user == nil || user.Id == 0 {
				return nil, protocolError()
			}
			c.rememberRef("user", user)
		}
		items := []map[string]any{}
		for _, admin := range reply.Admins {
			if admin == nil || admin.Uid == 0 || len(admin.GetTitle().GetValue()) > 4096 {
				return nil, protocolError()
			}
			items = append(items, map[string]any{"user_id": strconv.FormatUint(uint64(admin.Uid), 10), "title": admin.GetTitle().GetValue(), "permissions": permissionsJSON(admin.Permissions), "is_admin": admin.GetIsAdmin().GetValue()})
		}
		return json.Marshal(map[string]any{"admins": items})
	}
	reply := &wire.GroupBannedResponse{}
	if decode(data, reply) != nil || len(reply.BannedUsers) > 4096 {
		return nil, protocolError()
	}
	items := []map[string]any{}
	for _, entry := range reply.BannedUsers {
		if entry == nil || entry.BannedUser == nil || entry.BannedUser.Id == 0 || entry.BannerUser == nil || entry.BannerUser.Id == 0 {
			return nil, protocolError()
		}
		c.rememberRef("user", entry.BannedUser)
		c.rememberRef("user", entry.BannerUser)
		items = append(items, map[string]any{"user_id": strconv.FormatUint(uint64(entry.BannedUser.Id), 10), "banned_by": strconv.FormatUint(uint64(entry.BannerUser.Id), 10)})
	}
	return json.Marshal(map[string]any{"banned_users": items})
}

func groupInviteToken(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	if strings.Contains(token, "://") {
		u, err := url.Parse(token)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host != "ble.ir" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !strings.HasPrefix(u.Path, "/join/") {
			return "", groupRequestError("use a Bale invite token or a ble.ir/join URL")
		}
		token = strings.TrimPrefix(u.Path, "/join/")
	}
	if len(token) == 0 || len(token) > 1024 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(token) {
		return "", groupRequestError("invite token is invalid")
	}
	return token, nil
}

func validGroupUsername(value string, empty bool) bool {
	return (empty && value == "") || (len(value) > 0 && len(value) <= 64 && regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(value))
}

func (c *Client) groupJoinedJSON(data []byte, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	reply := &wire.GroupJoinResponse{}
	if decode(data, reply) != nil || reply.Group == nil || reply.Group.Id == 0 || len(reply.Group.Title) > 4096 {
		return nil, ambiguous()
	}
	if err := c.rememberConversationRefs(reply.Users, []*wire.Group{reply.Group}, reply.UserPeers, nil); err != nil {
		return nil, ambiguous()
	}
	peer, err := c.rememberGroupKind(&wire.PeerRef{Id: reply.Group.Id, AccessHash: reply.Group.AccessHash}, reply.Group.GroupType)
	if err != nil {
		return nil, ambiguous()
	}
	return json.Marshal(map[string]any{"peer": peer, "title": reply.Group.Title, "acknowledged": true})
}

func (c *Client) groupFullJSON(g *wire.GroupFullExtended, action int32) (json.RawMessage, error) {
	if g == nil || g.Id == 0 || g.CreateDate < 0 || g.GetMembersCount().GetValue() < 0 || len(g.Title)+len(g.GetAbout().GetValue())+len(g.GetNick().GetValue()) > 16384 {
		return nil, protocolError()
	}
	peer, err := c.rememberGroupKind(&wire.PeerRef{Id: g.Id, AccessHash: g.AccessHash}, g.GroupType)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"peer": peer, "title": g.Title, "description": g.GetAbout().GetValue(), "username": g.GetNick().GetValue(), "members_count": g.GetMembersCount().GetValue(), "is_member": g.GetIsMember().GetValue(), "group_type": g.GroupType, "action": action, "permissions": permissionsJSON(g.Permissions), "default_permissions": permissionsJSON(g.DefaultPermissions)})
}

// Channels share wire peer type2 with groups. Cache only provider-established
// subtype information; never reinterpret wire peer type3 (encrypted private).
func (c *Client) rememberGroupKind(ref *wire.PeerRef, kind int32) (domains.Peer, error) {
	if ref == nil || ref.Id == 0 || kind < 0 || kind > 2 {
		return domains.Peer{}, protocolError()
	}
	peer := domains.Peer{Type: "group", ID: strconv.FormatUint(uint64(ref.Id), 10)}
	if kind == 1 {
		peer.Type = "channel"
	}
	c.rememberRef(peer.Type, ref)
	return peer, nil
}

func (c *Client) canonicalPeer(peer domains.Peer) domains.Peer {
	if peer.Type != "group" {
		return peer
	}
	c.mu.Lock()
	_, channel := c.peerHashes["channel:"+peer.ID]
	c.mu.Unlock()
	if channel {
		peer.Type = "channel"
	}
	return peer
}

func (c *Client) forgetGroup(id uint32) {
	suffix := strconv.FormatUint(uint64(id), 10)
	c.mu.Lock()
	delete(c.peerHashes, "group:"+suffix)
	delete(c.peerHashes, "channel:"+suffix)
	c.mu.Unlock()
}
