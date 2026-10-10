package eitaameow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
)

var adminRights = []string{"change_info", "post_messages", "edit_messages", "delete_messages", "ban_users", "invite_users", "pin_messages", "add_admins", "anonymous", "manage_call", "other", "post_live"}

func boundedInt(lo, hi int64) domains.FieldSchema {
	return domains.FieldSchema{Type: "integer", Minimum: &lo, Maximum: &hi}
}
func stringList(min, max int, item domains.FieldSchema) domains.FieldSchema {
	return domains.FieldSchema{Type: "array", Items: &item, MinItems: min, MaxItems: max}
}
func extendedOperations() []domains.OperationContract {
	var ops []domains.OperationContract
	add := func(name, mode string, props map[string]domains.FieldSchema, required ...string) {
		description := "Eitaa " + name
		if name == "account.avatar" || name == "group.photo" {
			description += "; bounded JPEG/PNG source (at most 8 MiB), center-cropped and resized to a 512x512 JPEG"
		}
		ops = append(ops, domains.OperationContract{Operation: name, Mode: mode, Method: "POST", Path: "/operations/" + name, Description: description, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: props, Required: required}})
	}
	p := func() map[string]domains.FieldSchema { return map[string]domains.FieldSchema{"peer": peerField()} }
	user := func() map[string]domains.FieldSchema {
		return map[string]domains.FieldSchema{"peer": peerField(), "user": idField()}
	}
	add("contacts.add", "mutation", map[string]domains.FieldSchema{"user": idField(), "first_name": textField(256), "last_name": textField(256), "phone": textField(32)}, "user", "first_name")
	contact := domains.FieldSchema{Type: "object", Properties: map[string]domains.FieldSchema{"phone": {Type: "string", MinLength: 6, MaxLength: 16, Pattern: "^\\+?[1-9][0-9]{5,14}$"}, "first_name": textField(256), "last_name": textField(256)}, Required: []string{"phone", "first_name"}}
	add("contacts.import", "mutation", map[string]domains.FieldSchema{"contacts": stringList(1, 100, contact)}, "contacts")
	add("contacts.remove", "mutation", map[string]domains.FieldSchema{"user": idField()}, "user")
	add("contacts.resolve", "read", map[string]domains.FieldSchema{"username": {Type: "string", MinLength: 1, MaxLength: 64, Pattern: "^[A-Za-z][A-Za-z0-9_]{0,63}$"}}, "username")
	for _, name := range []string{"account.block", "account.unblock"} {
		add(name, "mutation", p(), "peer")
	}
	add("account.blocked", "read", map[string]domains.FieldSchema{"offset": boundedInt(0, 2147483647), "limit": boundedInt(1, 100)})
	add("account.name", "mutation", map[string]domains.FieldSchema{"first_name": textField(256), "last_name": textField(256)}, "first_name", "last_name")
	add("account.about", "mutation", map[string]domains.FieldSchema{"about": textField(512)}, "about")
	add("account.username", "mutation", map[string]domains.FieldSchema{"username": {Type: "string", MaxLength: 64, Pattern: "^[A-Za-z0-9_]*$"}}, "username")
	add("group.create", "mutation", map[string]domains.FieldSchema{"title": textField(512), "users": stringList(1, 100, idField())}, "title", "users")
	add("channel.create", "mutation", map[string]domains.FieldSchema{"title": textField(512), "about": textField(1024), "megagroup": {Type: "boolean"}}, "title")
	for _, name := range []string{"group.info", "group.members", "group.admins", "group.banned"} {
		props := p()
		props["offset"] = boundedInt(0, 2147483647)
		props["limit"] = boundedInt(1, 100)
		add(name, "read", props, "peer")
	}
	props := p()
	props["title"] = textField(512)
	add("group.name", "mutation", props, "peer", "title")
	for _, name := range []string{"group.add", "group.remove", "group.demote", "group.ban", "group.unban"} {
		add(name, "mutation", user(), "peer", "user")
	}
	props = user()
	rights := domains.FieldSchema{Type: "object", Properties: map[string]domains.FieldSchema{}, Required: adminRights}
	for _, k := range adminRights {
		rights.Properties[k] = domains.FieldSchema{Type: "boolean"}
	}
	props["rights"] = rights
	props["admin_title"] = textField(64)
	add("group.promote", "mutation", props, "peer", "user", "rights")
	for _, name := range []string{"group.join_public", "group.leave"} {
		add(name, "mutation", p(), "peer")
	}
	props = p()
	props["username"] = textField(64)
	add("group.username", "mutation", props, "peer", "username")
	add("group.link", "mutation", p(), "peer")
	for _, name := range []string{"group.preview", "group.join"} {
		mode := "read"
		if name == "group.join" {
			mode = "mutation"
		}
		add(name, mode, map[string]domains.FieldSchema{"token": {Type: "string", MinLength: 1, MaxLength: 512, Pattern: "^[A-Za-z0-9_-]+$"}}, "token")
	}
	add("folder.list", "read", map[string]domains.FieldSchema{})
	props = map[string]domains.FieldSchema{"id": boundedInt(2, 255), "title": textField(48), "include_peers": stringList(0, 100, peerField()), "exclude_peers": stringList(0, 100, peerField())}
	add("folder.set", "mutation", props, "id", "title", "include_peers", "exclude_peers")
	add("folder.remove", "mutation", map[string]domains.FieldSchema{"id": boundedInt(2, 255)}, "id")
	props = p()
	props["message_id"] = idField()
	add("poll.results", "read", props, "peer", "message_id")
	props = p()
	props["message_id"] = idField()
	props["options"] = stringList(0, 10, textField(88))
	add("poll.vote", "mutation", props, "peer", "message_id", "options")
	props = p()
	props["question"] = textField(1024)
	props["answers"] = stringList(2, 10, textField(256))
	props["multiple_choice"] = domains.FieldSchema{Type: "boolean"}
	props["public_voters"] = domains.FieldSchema{Type: "boolean"}
	add("send.poll", "mutation", props, "peer", "question", "answers")
	add("account.avatar", "mutation", map[string]domains.FieldSchema{"media_id": {Type: "string", MinLength: 1, MaxLength: 256}}, "media_id")
	props = p()
	props["media_id"] = domains.FieldSchema{Type: "string", MinLength: 1, MaxLength: 256}
	add("group.photo", "mutation", props, "peer", "media_id")
	item := domains.FieldSchema{Type: "object", Properties: map[string]domains.FieldSchema{"media_id": {Type: "string", MinLength: 1, MaxLength: 256}, "kind": {Type: "string", Enum: []json.RawMessage{json.RawMessage(`"image"`), json.RawMessage(`"video"`)}}, "caption": textField(16384)}, Required: []string{"media_id", "kind"}}
	props = p()
	props["items"] = stringList(2, 10, item)
	props["reply_to_message_id"] = idField()
	add("message.album", "mutation", props, "peer", "items")
	return ops
}

type importContact struct {
	Phone     string `json:"phone"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}
type extendedRequest struct {
	Contacts  []importContact `json:"contacts"`
	Peer      domains.Peer    `json:"peer"`
	User      string          `json:"user"`
	Users     []string        `json:"users"`
	FirstName string          `json:"first_name"`
	LastName  string          `json:"last_name"`
	Phone     string          `json:"phone"`
	Title     string          `json:"title"`
	About     string          `json:"about"`
	Username  string          `json:"username"`
	Megagroup bool            `json:"megagroup"`
	Offset    int             `json:"offset"`
	Limit     int             `json:"limit"`
	Rights    map[string]bool `json:"rights"`
	Rank      string          `json:"admin_title"`
	Token     string          `json:"token"`
	ID        int             `json:"id"`
	Include   []domains.Peer  `json:"include_peers"`
	Exclude   []domains.Peer  `json:"exclude_peers"`
	MessageID string          `json:"message_id"`
	Options   []string        `json:"options"`
	Question  string          `json:"question"`
	Answers   []string        `json:"answers"`
	Multiple  bool            `json:"multiple_choice"`
	Public    bool            `json:"public_voters"`
	ShortName string          `json:"short_name"`
	Items     []albumItem     `json:"items"`
	Reply     string          `json:"reply_to_message_id"`
	MediaID   string          `json:"media_id"`
}

func normalizeExtended(name string, raw json.RawMessage) error {
	var p extendedRequest
	if json.Unmarshal(raw, &p) != nil {
		return protocolError()
	}
	c := Contract{}
	if p.User != "" && !c.ValidateUserID(p.User) {
		return domains.E("INVALID_REQUEST", "invalid Eitaa user ID", 400)
	}
	seen := map[string]bool{}
	for _, id := range p.Users {
		if !c.ValidateUserID(id) || seen[id] {
			return domains.E("INVALID_REQUEST", "users must contain unique positive IDs", 400)
		}
		seen[id] = true
	}
	for _, peers := range [][]domains.Peer{p.Include, p.Exclude} {
		seen := map[string]bool{}
		for _, peer := range peers {
			if c.ValidatePeer(peer) != nil || seen[peer.Key()] {
				return domains.E("INVALID_REQUEST", "folder peers must be valid and unique", 400)
			}
			seen[peer.Key()] = true
		}
	}
	if strings.HasPrefix(name, "group.") && p.Peer.ID != "" && p.Peer.Type == "user" {
		return domains.E("INVALID_PEER", "group operation requires group or channel", 400)
	}
	for _, id := range []string{p.MessageID, p.Reply} {
		if id != "" {
			if _, err := messageNumber(id); err != nil {
				return err
			}
		}
	}
	for _, v := range p.Options {
		b, e := base64.StdEncoding.Strict().DecodeString(v)
		if e != nil || len(b) == 0 || len(b) > 64 {
			return domains.E("INVALID_REQUEST", "poll options must be base64 identifiers from poll results", 400)
		}
	}
	for _, item := range p.Items {
		if !utf8.ValidString(item.Caption) || len([]rune(item.Caption)) > 4096 {
			return domains.E("INVALID_REQUEST", "album captions must not exceed 4096 characters", 400)
		}
	}
	switch name {
	case "group.create", "channel.create", "group.name", "folder.set":
		if strings.TrimSpace(p.Title) == "" {
			return domains.E("INVALID_REQUEST", "title is required", 400)
		}
	case "send.poll":
		if strings.TrimSpace(p.Question) == "" {
			return domains.E("INVALID_REQUEST", "poll question is required", 400)
		}
		for _, a := range p.Answers {
			if strings.TrimSpace(a) == "" {
				return domains.E("INVALID_REQUEST", "poll answers must not be empty", 400)
			}
		}
	}
	return nil
}
func (c *Client) inputUser(ctx context.Context, id string) (object, error) {
	p, e := c.inputPeer(ctx, domains.Peer{Type: "user", ID: id})
	if e != nil {
		return nil, e
	}
	switch p.str("_") {
	case "inputPeerSelf":
		return object{"_": "inputUserSelf"}, nil
	case "inputPeerUser":
		return object{"_": "inputUser", "user_id": p.num("user_id"), "access_hash": p.num("access_hash")}, nil
	}
	return nil, protocolError()
}
func (c *Client) extendedCall(ctx context.Context, name string, raw json.RawMessage, rid int64) (json.RawMessage, bool, error) {
	known := false
	mutating := false
	for _, op := range extendedOperations() {
		if op.Operation == name {
			known = true
			mutating = op.Mode == "mutation"
			break
		}
	}
	if !known {
		return nil, false, nil
	}
	var p extendedRequest
	_ = json.Unmarshal(raw, &p)
	done := func(v json.RawMessage, e error) (json.RawMessage, bool, error) { return v, true, e }
	if name == "message.album" {
		v, e := c.sendAlbum(ctx, p.Peer, p.Items, p.Reply, strconv.FormatInt(rid, 10))
		return done(v, e)
	}
	if name == "account.avatar" || name == "group.photo" {
		v, e := c.setPhoto(ctx, name, p.Peer, p.MediaID, strconv.FormatInt(rid, 10))
		return done(v, e)
	}
	var peer, user object
	var err error
	if p.Peer.ID != "" {
		peer, err = c.inputPeer(ctx, p.Peer)
		if err != nil {
			return done(nil, err)
		}
	}
	if p.User != "" {
		user, err = c.inputUser(ctx, p.User)
		if err != nil {
			return done(nil, err)
		}
	}
	users := []object{}
	for _, id := range p.Users {
		u, e := c.inputUser(ctx, id)
		if e != nil {
			return done(nil, e)
		}
		users = append(users, u)
	}
	channel := peer.str("_") == "inputPeerChannel"
	needChannel := func() error {
		if !channel {
			return domains.Unsupported("operation requires a resolved channel or supergroup")
		}
		return nil
	}
	method := ""
	params := object{}
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	switch name {
	case "contacts.add":
		method = "contacts.addContact"
		params = object{"id": user, "first_name": p.FirstName, "last_name": p.LastName, "phone": p.Phone}
	case "contacts.import":
		method = "contacts.importContacts"
		contacts := []object{}
		for i, v := range p.Contacts {
			contacts = append(contacts, object{"_": "inputPhoneContact", "client_id": int64(i + 1), "phone": v.Phone, "first_name": v.FirstName, "last_name": v.LastName})
		}
		params = object{"contacts": contacts}
	case "contacts.remove":
		method = "contacts.deleteContacts"
		params = object{"id": []object{user}}
	case "contacts.resolve":
		method = "contacts.resolveUsername"
		params = object{"username": p.Username}
	case "account.block", "account.unblock":
		method = "contacts.block"
		if name == "account.unblock" {
			method = "contacts.unblock"
		}
		params = object{"id": peer}
	case "account.blocked":
		method = "contacts.getBlocked"
		params = object{"offset": p.Offset, "limit": limit}
	case "account.name":
		method = "account.updateProfile"
		params = object{"first_name": p.FirstName, "last_name": p.LastName}
	case "account.about":
		method = "account.updateProfile"
		params = object{"about": p.About}
	case "account.username":
		method = "account.updateUsername"
		params = object{"username": p.Username}
	case "group.create":
		method = "messages.createChat"
		params = object{"users": users, "title": p.Title}
	case "channel.create":
		method = "channels.createChannel"
		params = object{"title": p.Title, "about": p.About, "broadcast": !p.Megagroup, "megagroup": p.Megagroup}
	case "group.info", "group.members", "group.admins", "group.banned":
		if !channel && (name == "group.admins" || name == "group.banned") {
			return done(nil, domains.Unsupported("participant filters require a channel or supergroup"))
		}
		method = "messages.getFullChat"
		params = object{"chat_id": peer.num("chat_id")}
		if channel {
			method = "channels.getFullChannel"
			params = object{"channel": inputChannel(peer)}
			if name != "group.info" {
				method = "channels.getParticipants"
				filter := "channelParticipantsRecent"
				if name == "group.admins" {
					filter = "channelParticipantsAdmins"
				}
				if name == "group.banned" {
					filter = "channelParticipantsKicked"
				}
				filterObject := object{"_": filter}
				if name == "group.banned" {
					filterObject["q"] = ""
				}
				params = object{"channel": inputChannel(peer), "filter": filterObject, "offset": p.Offset, "limit": limit, "hash": 0}
			}
		}
	case "group.name":
		method = "messages.editChatTitle"
		params = object{"chat_id": peer.num("chat_id"), "title": p.Title}
		if channel {
			method = "channels.editTitle"
			params = object{"channel": inputChannel(peer), "title": p.Title}
		}
	case "group.add":
		method = "messages.addChatUser"
		params = object{"chat_id": peer.num("chat_id"), "user_id": user, "fwd_limit": 0}
		if channel {
			method = "channels.inviteToChannel"
			params = object{"channel": inputChannel(peer), "users": []object{user}}
		}
	case "group.remove":
		if channel {
			return done(nil, domains.Unsupported("use group.ban for channel membership removal"))
		}
		method = "messages.deleteChatUser"
		params = object{"chat_id": peer.num("chat_id"), "user_id": user}
	case "group.promote", "group.demote":
		if e := needChannel(); e != nil {
			return done(nil, e)
		}
		rights := object{"_": "chatAdminRights"}
		if name == "group.promote" {
			for k, v := range p.Rights {
				rights[k] = v
			}
		}
		method = "channels.editAdmin"
		params = object{"channel": inputChannel(peer), "user_id": user, "admin_rights": rights, "rank": p.Rank}
	case "group.ban", "group.unban":
		if e := needChannel(); e != nil {
			return done(nil, e)
		}
		target, e := c.inputPeer(ctx, domains.Peer{Type: "user", ID: p.User})
		if e != nil {
			return done(nil, e)
		}
		method = "channels.editBanned"
		params = object{"channel": inputChannel(peer), "participant": target, "banned_rights": object{"_": "chatBannedRights", "view_messages": name == "group.ban", "until_date": 0}}
	case "group.join_public", "group.leave", "group.username":
		if name == "group.leave" && !channel {
			method = "messages.deleteChatUser"
			params = object{"chat_id": peer.num("chat_id"), "user_id": object{"_": "inputUserSelf"}}
			break
		}
		if e := needChannel(); e != nil {
			return done(nil, e)
		}
		method = "channels.joinChannel"
		params = object{"channel": inputChannel(peer)}
		if name == "group.leave" {
			method = "channels.leaveChannel"
		}
		if name == "group.username" {
			method = "channels.updateUsername"
			params["username"] = p.Username
		}
	case "group.link":
		// Reuse the account-visible permanent invitation before requesting a
		// new export. Some Eitaa deployments return Layer122 invitations in
		// full-chat reads but reject the newer export constructor outright.
		if invite, e := c.existingGroupInvite(ctx, peer); e != nil {
			return done(nil, e)
		} else if invite != nil {
			out, err := json.Marshal(invite)
			return done(out, err)
		}
		method = "messages.exportChatInvite"
		params = object{"peer": peer}
	case "group.preview", "group.join":
		method = "messages.checkChatInvite"
		if name == "group.join" {
			method = "messages.importChatInvite"
		}
		params = object{"hash": p.Token}
	case "folder.list":
		method = "messages.getDialogFilters"
	case "folder.set", "folder.remove":
		method = "messages.updateDialogFilter"
		params = object{"id": p.ID}
		if name == "folder.set" {
			lists := [][]object{{}, {}}
			for i, ps := range [][]domains.Peer{p.Include, p.Exclude} {
				for _, v := range ps {
					ref, e := c.inputPeer(ctx, v)
					if e != nil {
						return done(nil, e)
					}
					lists[i] = append(lists[i], ref)
				}
			}
			params["filter"] = object{"_": "dialogFilter", "id": p.ID, "title": p.Title, "pinned_peers": []object{}, "include_peers": lists[0], "exclude_peers": lists[1]}
		}
	case "poll.results", "poll.vote":
		id, _ := messageNumber(p.MessageID)
		method = "messages.getPollResults"
		params = object{"peer": peer, "msg_id": id}
		if name == "poll.vote" {
			method = "messages.sendVote"
			opts := [][]byte{}
			for _, s := range p.Options {
				v, _ := base64.StdEncoding.Strict().DecodeString(s)
				opts = append(opts, v)
			}
			params["options"] = opts
		}
	case "send.poll":
		answers := []object{}
		for i, a := range p.Answers {
			answers = append(answers, object{"_": "pollAnswer", "text": a, "option": []byte{byte(i)}})
		}
		method = "messages.sendMedia"
		params = object{"peer": peer, "random_id": rid, "message": "", "media": object{"_": "inputMediaPoll", "poll": object{"_": "poll", "id": rid, "question": p.Question, "answers": answers, "multiple_choice": p.Multiple, "public_voters": p.Public}}}
	default:
		return done(nil, domains.Unsupported(name))
	}
	response, err := c.invoke(ctx, method, params, mutating, false)
	if err != nil {
		return done(nil, err)
	}
	if acknowledged, ok := response["acknowledged"].(bool); mutating && ok && !acknowledged {
		return done(nil, domains.E("PROVIDER_REJECTED", "Eitaa did not acknowledge this operation", 502))
	}
	if err = c.rememberEntities(ctx, response); err != nil {
		if mutating {
			return done(nil, &domains.Error{Code: "SEND_UNKNOWN", Message: "operation response could not be durably accepted", HTTP: 502, Ambiguous: true})
		}
		return done(nil, err)
	}
	if name == "send.poll" {
		v, e := sendResult(response, rid)
		if e != nil {
			return done(nil, e)
		}
		out, e := json.Marshal(v)
		return done(out, e)
	}
	out, e := json.Marshal(c.projectOperation(response))
	return done(out, e)
}

// Native opaque references never pass through to public responses. Projection
// follows an allowlist of reviewed scalar fields and nested collection shapes.
func projectOperation(o object) object             { return projectOperationFor(nil, o) }
func (c *Client) projectOperation(o object) object { return projectOperationFor(c, o) }
func projectOperationFor(c *Client, o object) object {
	out := object{}
	if o == nil {
		return out
	}
	for _, key := range []string{"id", "user_id", "poll_id", "admin_id", "channel_id", "chat_id", "inviter_id", "client_id"} {
		if _, ok := o[key]; ok {
			out[key] = strconv.FormatInt(o.num(key), 10)
		}
	}
	for _, key := range []string{"first_name", "last_name", "username", "title", "about", "short_name", "question", "text", "link", "mime_type", "file_name", "alt"} {
		if s, ok := o[key].(string); ok {
			out[key] = s
		}
	}
	for _, key := range []string{"count", "participants_count", "admins_count", "total_voters", "voters", "usage", "usage_limit", "date", "expire_date"} {
		if v, ok := o[key]; ok {
			if n, e := integer(v); e == nil {
				out[key] = n
			}
		}
	}
	for _, key := range []string{"acknowledged", "closed", "multiple_choice", "public_voters", "chosen", "correct", "revoked", "permanent", "contacts", "non_contacts", "groups", "broadcasts", "bots", "admin", "exclude_muted", "exclude_read", "exclude_archived"} {
		if b, ok := o[key].(bool); ok {
			out[key] = b
		}
	}
	if b, ok := o["option"].([]byte); ok {
		out["option"] = base64.StdEncoding.EncodeToString(b)
	}
	for _, key := range []string{"users", "chats", "items", "sets", "documents", "gifs", "attributes", "participants", "answers", "results", "updates", "imported", "include_peers", "exclude_peers", "pinned_peers"} {
		if _, ok := o[key]; !ok {
			continue
		}
		if child := asObject(o[key]); child != nil {
			out[key] = projectOperationFor(c, child)
			continue
		}
		items := []object{}
		for _, item := range asObjects(o[key]) {
			items = append(items, projectOperationFor(c, item))
		}
		out[key] = items
	}
	for _, key := range []string{"full_chat", "chat", "user", "set", "poll"} {
		if child := asObject(o[key]); child != nil {
			out[key] = projectOperationFor(c, child)
		}
	}
	if peer := asObject(o["peer"]); peer != nil {
		if p, e := publicPeer(peer); e == nil {
			out["peer"] = p
		}
	}
	switch o.str("_") {
	case "inputPeerUser":
		out["peer"] = domains.Peer{Type: "user", ID: strconv.FormatInt(o.num("user_id"), 10)}
	case "inputPeerChat":
		out["peer"] = domains.Peer{Type: "group", ID: strconv.FormatInt(o.num("chat_id"), 10)}
	case "inputPeerChannel":
		out["peer"] = domains.Peer{Type: "channel", ID: strconv.FormatInt(o.num("channel_id"), 10)}
	}
	// Entity identities and cached input peers must use the same public
	// namespace as event/history projections.
	switch o.str("_") {
	case "chat", "chatForbidden", "channel", "channelForbidden":
		if peer, err := entityPeer(o); err == nil {
			out["peer"] = peer
			out["id"] = peer.ID
		}
	case "chatFull":
		out["peer"] = domains.Peer{Type: "group", ID: strconv.FormatInt(o.num("id"), 10)}
	case "channelFull":
		out["peer"] = domains.Peer{Type: "channel", ID: strconv.FormatInt(o.num("id"), 10)}
	}
	if peer, ok := out["peer"].(domains.Peer); ok && c != nil {
		peer = c.normalizedPeer(peer)
		out["peer"] = peer
		if o.str("_") == "channelFull" {
			out["id"] = peer.ID
		}
	}
	if o.str("_") == "updates" || o.str("_") == "updatesCombined" {
		out["acknowledged"] = true
	}
	return out
}
