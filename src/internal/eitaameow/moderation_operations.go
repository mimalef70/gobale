package eitaameow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
)

type moderationRequest struct {
	Peer        domains.Peer `json:"peer"`
	Participant domains.Peer `json:"participant"`
	Group       domains.Peer `json:"group_peer"`
	User        string       `json:"admin_id"`
	Seconds     int64        `json:"seconds"`
	Hidden      bool         `json:"hidden"`
	Archived    bool         `json:"archived"`
	Revoked     bool         `json:"revoked"`
	Link        string       `json:"link"`
	Expire      *int64       `json:"expire_date"`
	Usage       *int64       `json:"usage_limit"`
	Limit       int          `json:"limit"`
	Cursor      string       `json:"cursor"`
	MessageID   string       `json:"message_id"`
	ReadMaxID   string       `json:"read_max_id"`
}

func moderationOperations() []domains.OperationContract {
	var out []domains.OperationContract
	add := func(name, mode, description string, fields map[string]domains.FieldSchema, required ...string) {
		out = append(out, domains.OperationContract{Operation: name, Mode: mode, Description: description, Verification: "offline; live-unverified", Method: "POST", Path: "/operations/" + name, Request: domains.FieldSchema{Type: "object", Properties: fields, Required: required}})
	}
	peer := func() map[string]domains.FieldSchema { return map[string]domains.FieldSchema{"peer": peerField()} }
	p := peer()
	p["participant"] = peerField()
	add("group.participant", "read", "Read one participant through account-owned channel references", p, "peer", "participant")
	add("group.discussion.candidates", "read", "List provider-eligible groups for channel discussion linking", map[string]domains.FieldSchema{})
	p = peer()
	p["group_peer"] = peerField()
	add("group.discussion.set", "mutation", "Link an existing supergroup; migrate classic groups and change history visibility explicitly first when required", p, "peer", "group_peer")
	add("group.discussion.unlink", "mutation", "Remove a channel's linked discussion group", peer(), "peer")
	p = peer()
	p["seconds"] = boundedInt(0, 86400)
	add("group.slowmode", "mutation", "Set bounded slow-mode seconds on an existing supergroup; zero disables it", p, "peer", "seconds")
	p = peer()
	p["hidden"] = domains.FieldSchema{Type: "boolean"}
	add("group.history_visibility", "mutation", "Explicitly change pre-join history visibility on a supergroup", p, "peer", "hidden")
	add("group.upgrade", "mutation", "Explicitly migrate a classic group to a supergroup and return the new peer; uncertain results are never retried", peer(), "peer")
	add("message.unpin_all", "mutation", "Unpin through at most 32 journaled provider batches; incomplete results stay unknown", peer(), "peer")
	p = peer()
	p["message_id"] = idField()
	add("group.discussion.message", "read", "Read the provider-linked discussion messages with their actual peers", p, "peer", "message_id")
	p = peer()
	p["message_id"] = idField()
	p["read_max_id"] = idField()
	add("group.discussion.read", "mutation", "Mark an explicit discussion message boundary read", p, "peer", "message_id", "read_max_id")
	p = peer()
	p["archived"] = domains.FieldSchema{Type: "boolean"}
	add("chat.archive", "mutation", "Move a chat between the provider's archive and main folder", p, "peer", "archived")
	for _, name := range []string{"group.invites", "group.invite.get", "group.invite.create", "group.invite.edit", "group.invite.revoke", "group.invite.delete", "group.invites.delete_revoked", "group.invites.admins", "group.invite.importers"} {
		p = peer()
		req := []string{"peer"}
		mode := "mutation"
		switch name {
		case "group.invites":
			mode = "read"
			p["admin_id"] = idField()
			p["revoked"] = domains.FieldSchema{Type: "boolean"}
			p["limit"] = boundedInt(1, 100)
			p["cursor"] = textField(4096)
		case "group.invite.get", "group.invite.importers":
			mode = "read"
		case "group.invites.admins":
			mode = "read"
		case "group.invites.delete_revoked":
			p["admin_id"] = idField()
		}
		switch name {
		case "group.invite.get", "group.invite.edit", "group.invite.revoke", "group.invite.delete", "group.invite.importers":
			p["link"] = textField(512)
			req = append(req, "link")
		}
		if name == "group.invite.create" || name == "group.invite.edit" {
			p["expire_date"] = boundedInt(0, 2147483647)
			p["usage_limit"] = boundedInt(0, 2147483647)
		}
		if name == "group.invite.importers" {
			p["limit"] = boundedInt(1, 100)
			p["cursor"] = textField(4096)
		}
		add(name, mode, "Manage an explicit account-scoped exported group invitation", p, req...)
	}
	return out
}
func knownModeration(name string) (bool, bool) {
	for _, op := range moderationOperations() {
		if op.Operation == name {
			return true, op.Mode == "mutation"
		}
	}
	return false, false
}
func validInviteLink(s string) bool {
	if len(s) == 0 || len(s) > 512 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func normalizeModeration(name string, raw json.RawMessage) error {
	known, _ := knownModeration(name)
	if !known {
		return nil
	}
	var p moderationRequest
	if json.Unmarshal(raw, &p) != nil {
		return domains.E("INVALID_REQUEST", "invalid moderation request", 400)
	}
	if strings.HasPrefix(name, "group.") && name != "group.discussion.candidates" && p.Peer.Type != "group" && p.Peer.Type != "channel" {
		return domains.E("INVALID_PEER", "operation requires a group or channel", 400)
	}
	if p.Participant.ID != "" && (Contract{}).ValidatePeer(p.Participant) != nil {
		return domains.E("INVALID_PEER", "invalid participant peer", 400)
	}
	if p.Group.ID != "" && ((Contract{}).ValidatePeer(p.Group) != nil || p.Group.Type != "group") {
		return domains.E("INVALID_PEER", "discussion destination must be a group", 400)
	}
	if name == "group.discussion.set" || name == "group.discussion.unlink" {
		if p.Peer.Type != "channel" {
			return domains.E("INVALID_PEER", "discussion source must be a broadcast channel", 400)
		}
	}
	if (name == "group.slowmode" || name == "group.history_visibility" || name == "group.upgrade") && p.Peer.Type != "group" {
		return domains.E("INVALID_PEER", "operation requires a group", 400)
	}
	if p.User != "" && !(Contract{}).ValidateUserID(p.User) {
		return domains.E("INVALID_USER_ID", "administrator must be a positive decimal ID", 400)
	}
	if p.Link != "" && !validInviteLink(p.Link) {
		return domains.E("INVALID_REQUEST", "invalid invitation link", 400)
	}
	switch name {
	case "group.invite.get", "group.invite.edit", "group.invite.revoke", "group.invite.delete", "group.invite.importers":
		if p.Link == "" {
			return domains.E("INVALID_REQUEST", "invitation link is required", 400)
		}
	}
	if name == "group.invite.edit" && p.Expire == nil && p.Usage == nil {
		return domains.E("INVALID_REQUEST", "at least one invitation property is required", 400)
	}
	if p.ReadMaxID != "" {
		if _, err := messageNumber(p.ReadMaxID); err != nil {
			return err
		}
	}
	return nil
}
func moderationUnknown() error {
	return &domains.Error{Code: "SEND_UNKNOWN", Message: "Eitaa moderation outcome is incomplete; automatic retry is disabled", HTTP: 502, Ambiguous: true}
}
func (c *Client) moderationCall(ctx context.Context, name string, raw json.RawMessage, rid int64) (json.RawMessage, bool, error) {
	known, write := knownModeration(name)
	if !known {
		return nil, false, nil
	}
	done := func(v object, err error) (json.RawMessage, bool, error) {
		if err != nil {
			return nil, true, err
		}
		b, e := json.Marshal(v)
		return b, true, e
	}
	var p moderationRequest
	_ = json.Unmarshal(raw, &p)
	var input object
	var err error
	if p.Peer.ID != "" {
		input, err = c.inputPeer(ctx, p.Peer)
		if err != nil {
			return done(nil, err)
		}
	}
	channel := func() (object, error) {
		if input.str("_") != "inputPeerChannel" {
			return nil, domains.Unsupported("operation requires an existing channel or supergroup")
		}
		return inputChannel(input), nil
	}
	method := ""
	params := object{}
	expected := ""
	switch name {
	case "group.participant":
		ch, e := channel()
		if e != nil {
			return done(nil, e)
		}
		participant, e := c.inputPeer(ctx, p.Participant)
		if e != nil {
			return done(nil, e)
		}
		method = "channels.getParticipant"
		params = object{"channel": ch, "participant": participant}
	case "group.discussion.candidates":
		method = "channels.getGroupsForDiscussion"
	case "group.discussion.set", "group.discussion.unlink":
		ch, e := channel()
		if e != nil {
			return done(nil, e)
		}
		group := object{"_": "inputChannelEmpty"}
		if name == "group.discussion.set" {
			g, e := c.inputPeer(ctx, p.Group)
			if e != nil {
				return done(nil, e)
			}
			if g.str("_") != "inputPeerChannel" {
				return done(nil, domains.Unsupported("discussion destination requires explicit supergroup migration first"))
			}
			group = inputChannel(g)
		}
		method = "channels.setDiscussionGroup"
		params = object{"broadcast": ch, "group": group}
		expected = "bool"
	case "group.slowmode", "group.history_visibility":
		ch, e := channel()
		if e != nil {
			return done(nil, e)
		}
		method = "channels.toggleSlowMode"
		params = object{"channel": ch, "seconds": p.Seconds}
		if name == "group.history_visibility" {
			method = "channels.togglePreHistoryHidden"
			params = object{"channel": ch, "enabled": p.Hidden}
		}
		expected = "updates"
	case "group.upgrade":
		if input.str("_") != "inputPeerChat" {
			return done(nil, domains.Unsupported("only a classic group can be migrated"))
		}
		method = "messages.migrateChat"
		params = object{"chat_id": input.num("chat_id")}
		expected = "updates"
	case "message.unpin_all":
		return done(c.unpinAll(ctx, input, rid))
	case "group.discussion.message", "group.discussion.read":
		id, _ := messageNumber(p.MessageID)
		method = "messages.getDiscussionMessage"
		params = object{"peer": input, "msg_id": id}
		if name == "group.discussion.read" {
			method = "messages.readDiscussion"
			maximum, _ := messageNumber(p.ReadMaxID)
			params["read_max_id"] = maximum
			expected = "bool"
		}
	case "chat.archive":
		folder := 0
		if p.Archived {
			folder = 1
		}
		method = "folders.editPeerFolders"
		params = object{"folder_peers": []object{{"_": "inputFolderPeer", "peer": input, "folder_id": folder}}}
		expected = "updates"
	default:
		result, e := c.inviteOperation(ctx, name, p, input, write)
		return done(result, e)
	}
	response, err := c.invoke(ctx, method, params, write, false)
	if err != nil {
		return done(nil, err)
	}
	if expected != "" {
		if expected == "bool" && response["acknowledged"] != true || expected == "updates" && !isUpdatesResponse(response) {
			return done(nil, moderationUnknown())
		}
		if err = c.rememberEntities(ctx, response); err != nil {
			return done(nil, moderationUnknown())
		}
		out := object{"acknowledged": true}
		if name == "group.upgrade" {
			peers := []domains.Peer{}
			for _, ch := range asObjects(response["chats"]) {
				if ch.str("_") == "channel" && ch["megagroup"] == true && ch.num("id") > 0 {
					peers = append(peers, domains.Peer{Type: "group", ID: supergroupPrefix + strconv.FormatInt(ch.num("id"), 10)})
				}
			}
			if len(peers) != 1 {
				return done(nil, moderationUnknown())
			}
			out["peer"] = peers[0]
		}
		return done(out, nil)
	}
	if err = c.rememberEntities(ctx, response); err != nil {
		return done(nil, err)
	}
	switch name {
	case "group.participant":
		item := asObject(response["participant"])
		var actual domains.Peer
		if item.num("user_id") > 0 {
			actual = domains.Peer{Type: "user", ID: strconv.FormatInt(item.num("user_id"), 10)}
		} else {
			actual, err = publicPeer(asObject(item["peer"]))
			if err != nil {
				return done(nil, err)
			}
		}
		actual = c.normalizedPeer(actual)
		if actual != p.Participant {
			return done(nil, protocolError())
		}
		role := map[string]string{"channelParticipant": "member", "channelParticipantSelf": "self", "channelParticipantCreator": "creator", "channelParticipantAdmin": "admin", "channelParticipantBanned": "banned", "channelParticipantLeft": "left"}[item.str("_")]
		if role == "" {
			return done(nil, protocolError())
		}
		result := c.projectOperation(item)
		result["role"] = role
		result["peer"] = actual
		for _, key := range []string{"admin_rights", "banned_rights"} {
			if rights := asObject(item[key]); rights != nil {
				safe := object{}
				for name, value := range rights {
					if yes, ok := value.(bool); ok {
						safe[name] = yes
					}
				}
				if _, ok := rights["until_date"]; ok {
					safe["until_date"] = rights.num("until_date")
				}
				result[key] = safe
			}
		}
		return done(result, nil)
	case "group.discussion.candidates":
		items := []object{}
		if len(asObjects(response["chats"])) > 1000 {
			return done(nil, protocolError())
		}
		for _, ch := range asObjects(response["chats"]) {
			if ch.num("id") <= 0 || (ch.str("_") != "chat" && ch.str("_") != "channel") {
				return done(nil, protocolError())
			}
			item := c.projectOperation(ch)
			publicPeer, err := entityPeer(ch)
			if err != nil {
				return done(nil, err)
			}
			item["peer"] = publicPeer
			items = append(items, item)
		}
		return done(object{"items": items}, nil)
	case "group.discussion.message":
		if response.str("_") != "messages.discussionMessage" || response.num("unread_count") < 0 || len(asObjects(response["messages"])) > 1000 {
			return done(nil, protocolError())
		}
		items := []object{}
		for _, m := range asObjects(response["messages"]) {
			peer, e := c.messagePeer(m)
			if e != nil {
				return done(nil, e)
			}
			event, e := c.projectReadEvent(ctx, m, peer)
			if e != nil {
				return done(nil, e)
			}
			items = append(items, object{"peer": event.Peer, "message": event.Message, "payload": event.Payload})
		}
		out := object{"items": items, "unread_count": response.num("unread_count")}
		for _, key := range []string{"max_id", "read_inbox_max_id", "read_outbox_max_id"} {
			if _, ok := response[key]; ok {
				if response.num(key) < 0 {
					return done(nil, protocolError())
				}
				out[key] = strconv.FormatInt(response.num(key), 10)
			}
		}
		return done(out, nil)
	}
	return done(nil, protocolError())
}
func (c *Client) unpinAll(ctx context.Context, peer object, rid int64) (object, error) {
	for n := 1; n <= 32; n++ {
		stage := domains.OperationStage{Number: n, Name: "message.unpin_all", State: "started", Nonce: strconv.FormatInt(rid, 10)}
		if err := domains.RecordOperationStage(ctx, stage); err != nil {
			if n > 1 {
				return nil, moderationUnknown()
			}
			return nil, err
		}
		response, err := c.invoke(ctx, "messages.unpinAllMessages", object{"peer": peer}, true, false)
		if err == nil && (response.str("_") != "messages.affectedHistory" || response.num("offset") < 0) {
			err = moderationUnknown()
		}
		stage.State = "succeeded"
		if err != nil {
			stage.State = "unknown"
		}
		stage.Data, _ = json.Marshal(object{"offset": response.num("offset")})
		if recordErr := domains.RecordOperationStage(ctx, stage); recordErr != nil {
			return nil, moderationUnknown()
		}
		if err != nil {
			if n > 1 {
				return nil, moderationUnknown()
			}
			return nil, err
		}
		if response.num("offset") == 0 {
			return object{"acknowledged": true, "batches": n}, nil
		}
	}
	return nil, moderationUnknown()
}

type inviteOffset struct {
	Date  int64  `json:"date"`
	Value string `json:"value"`
}

func (c *Client) inviteOperation(ctx context.Context, name string, p moderationRequest, input object, write bool) (object, error) {
	method := ""
	params := object{"peer": input}
	expected := ""
	admin := object{"_": "inputUserSelf"}
	var err error
	if p.User != "" {
		admin, err = c.inputUser(ctx, p.User)
		if err != nil {
			return nil, err
		}
	}
	kindData, _ := json.Marshal(object{"name": name, "peer": p.Peer.Key(), "admin": p.User, "revoked": p.Revoked, "link": p.Link})
	sum := sha256.Sum256(kindData)
	kind := "invite:" + hex.EncodeToString(sum[:])
	cursor, err := c.decodeValueCursor(p.Cursor, kind)
	if err != nil {
		return nil, err
	}
	offset := inviteOffset{}
	if cursor != "" {
		if json.Unmarshal([]byte(cursor), &offset) != nil || offset.Date <= 0 || offset.Date > 2147483647 || !validInviteLink(offset.Value) {
			return nil, cursorInvalid()
		}
	}
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	switch name {
	case "group.invites":
		method = "messages.getExportedChatInvites"
		params["admin_id"] = admin
		params["revoked"] = p.Revoked
		params["limit"] = limit
		if cursor != "" {
			params["offset_date"] = offset.Date
			params["offset_link"] = offset.Value
		}
	case "group.invite.get":
		method = "messages.getExportedChatInvite"
		params["link"] = p.Link
	case "group.invite.create":
		method = "messages.exportChatInvite"
	case "group.invite.edit", "group.invite.revoke":
		method = "messages.editExportedChatInvite"
		params["link"] = p.Link
		if name == "group.invite.revoke" {
			params["revoked"] = true
		}
	case "group.invite.delete":
		method = "messages.deleteExportedChatInvite"
		params["link"] = p.Link
		expected = "bool"
	case "group.invites.delete_revoked":
		method = "messages.deleteRevokedExportedChatInvites"
		params["admin_id"] = admin
		expected = "bool"
	case "group.invites.admins":
		method = "messages.getAdminsWithInvites"
	case "group.invite.importers":
		method = "messages.getChatInviteImporters"
		params["link"] = p.Link
		params["limit"] = limit
		params["offset_date"] = int64(0)
		params["offset_user"] = object{"_": "inputUserEmpty"}
		if cursor != "" {
			if !(Contract{}).ValidateUserID(offset.Value) {
				return nil, cursorInvalid()
			}
			user, e := c.inputUser(ctx, offset.Value)
			if e != nil {
				return nil, e
			}
			params["offset_date"] = offset.Date
			params["offset_user"] = user
		}
	default:
		return nil, domains.Unsupported(name)
	}
	if p.Expire != nil {
		params["expire_date"] = *p.Expire
	}
	if p.Usage != nil {
		params["usage_limit"] = *p.Usage
	}
	response, err := c.invoke(ctx, method, params, write, false)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (object, error) {
		if write {
			return nil, moderationUnknown()
		}
		return nil, err
	}
	if expected == "bool" {
		if response["acknowledged"] != true {
			return fail(protocolError())
		}
		return object{"acknowledged": true}, nil
	}
	if err = c.rememberEntities(ctx, response); err != nil {
		return fail(err)
	}
	if name == "group.invites" || name == "group.invite.importers" {
		field := "invites"
		if name == "group.invite.importers" {
			field = "importers"
		}
		rows, ok := response[field].([]any)
		if !ok || len(rows) > limit || response.num("count") < 0 {
			return nil, protocolError()
		}
		items := []object{}
		last := inviteOffset{}
		seen := map[string]bool{}
		for _, v := range rows {
			row := asObject(v)
			item := c.projectOperation(row)
			value := row.str("link")
			if field == "importers" {
				if row.num("user_id") <= 0 || row.num("date") <= 0 {
					return nil, protocolError()
				}
				value = strconv.FormatInt(row.num("user_id"), 10)
			} else if _, e := projectInvite(row); e != nil {
				return nil, e
			}
			if seen[value] {
				return nil, protocolError()
			}
			seen[value] = true
			last = inviteOffset{Date: row.num("date"), Value: value}
			items = append(items, item)
		}
		out := object{"items": items, "count": response.num("count"), "has_more": false}
		if len(rows) == limit && len(rows) > 0 {
			if last.Date <= 0 || last == offset {
				return nil, cursorInvalid()
			}
			raw, _ := json.Marshal(last)
			out["next_cursor"] = c.encodeValueCursor(kind, string(raw))
			out["has_more"] = true
		}
		return out, nil
	}
	if name == "group.invites.admins" {
		rows, ok := response["admins"].([]any)
		if !ok || len(rows) > 1000 {
			return nil, protocolError()
		}
		items := []object{}
		for _, v := range rows {
			row := asObject(v)
			if row.num("admin_id") <= 0 || row.num("invites_count") < 0 || row.num("revoked_invites_count") < 0 {
				return nil, protocolError()
			}
			items = append(items, object{"admin_id": strconv.FormatInt(row.num("admin_id"), 10), "invites_count": row.num("invites_count"), "revoked_invites_count": row.num("revoked_invites_count")})
		}
		return object{"items": items}, nil
	}
	invite := response
	if name != "group.invite.create" {
		invite = asObject(response["invite"])
	}
	projected, err := projectInvite(invite)
	if err != nil {
		return fail(err)
	}
	if p.Link != "" && invite.str("link") != p.Link {
		return fail(protocolError())
	}
	out := object{"invite": projected}
	if write {
		out["acknowledged"] = true
	}
	if replacement := asObject(response["new_invite"]); replacement != nil {
		next, e := projectInvite(replacement)
		if e != nil {
			return fail(e)
		}
		out["new_invite"] = next
	}
	return out, nil
}
func projectInvite(invite object) (object, error) {
	if invite.str("_") != "chatInviteExported" || !validInviteLink(invite.str("link")) || invite.num("admin_id") <= 0 || invite.num("date") <= 0 {
		return nil, protocolError()
	}
	return projectOperation(invite), nil
}
