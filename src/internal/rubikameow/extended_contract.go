package rubikameow

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

var allowedAccess = []string{"ChangeInfo", "ViewInfo", "ViewMembers", "ViewAdmins", "PinMessages", "SendMessages", "EditAllMessages", "DeleteGlobalAllMessages", "AddMember", "BanMember", "SetAdmin", "SetJoinLink", "SetMemberAccess", "ViewMessages", "DeleteLocalMessages", "EditMyMessages", "DeleteGlobalMyMessages"}

func enumField(values ...string) domains.FieldSchema {
	f := domains.FieldSchema{Type: "string"}
	for _, v := range values {
		b, _ := json.Marshal(v)
		f.Enum = append(f.Enum, b)
	}
	return f
}
func boundedInt(lo, hi int64) domains.FieldSchema {
	return domains.FieldSchema{Type: "integer", Minimum: &lo, Maximum: &hi}
}
func arrayField(lo, hi int, item domains.FieldSchema) domains.FieldSchema {
	return domains.FieldSchema{Type: "array", Items: &item, MinItems: lo, MaxItems: hi}
}
func extendedOperations() []domains.OperationContract {
	var out []domains.OperationContract
	add := func(name, mode string, p map[string]domains.FieldSchema, required ...string) {
		out = append(out, domains.OperationContract{Operation: name, Method: "POST", Path: "/operations/" + name, Mode: mode, Schedulable: strings.HasPrefix(name, "send."), Description: "Rubika " + name, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: p, Required: required}})
	}
	peer := func() map[string]domains.FieldSchema { return map[string]domains.FieldSchema{"peer": peerField()} }
	text := func(n int) domains.FieldSchema { f := textField(n); f.MinLength = 1; return f }
	add("contacts.add", "mutation", map[string]domains.FieldSchema{"phone": {Type: "string", MinLength: 6, MaxLength: 16, Pattern: `^\+?[1-9][0-9]{5,14}$`}, "first_name": text(256), "last_name": textField(256)}, "phone", "first_name")
	add("contacts.remove", "mutation", map[string]domains.FieldSchema{"user_id": idField()}, "user_id")
	add("contacts.resolve", "read", map[string]domains.FieldSchema{"username": {Type: "string", MinLength: 1, MaxLength: 64, Pattern: "^[A-Za-z][A-Za-z0-9_]*$"}}, "username")
	add("search.global", "read", map[string]domains.FieldSchema{"query": text(512)}, "query")
	p := peer()
	p["query"], p["search_type"] = text(512), enumField("Text", "Hashtag")
	add("chat.search", "read", p, "peer", "query", "search_type")
	p = peer()
	p["message_ids"] = arrayField(1, 100, idField())
	add("chat.get_by_ids", "read", p, "peer", "message_ids")
	p = peer()
	p["activity"] = enumField("Typing", "Uploading", "Recording")
	add("chat.activity", "mutation", p, "peer", "activity")
	add("account.name", "mutation", map[string]domains.FieldSchema{"first_name": text(256), "last_name": textField(256)}, "first_name", "last_name")
	add("account.about", "mutation", map[string]domains.FieldSchema{"about": textField(1024)}, "about")
	add("account.username", "mutation", map[string]domains.FieldSchema{"username": {Type: "string", MaxLength: 64, Pattern: "^[A-Za-z0-9_]*$"}}, "username")
	add("group.create", "mutation", map[string]domains.FieldSchema{"title": text(512), "users": arrayField(1, 100, idField())}, "title", "users")
	add("channel.create", "mutation", map[string]domains.FieldSchema{"title": text(512), "description": textField(1024), "channel_type": enumField("Public", "Private"), "users": arrayField(0, 100, idField())}, "title", "channel_type", "users")
	for _, name := range []string{"group.name", "group.description", "group.username"} {
		p := peer()
		field := "title"
		if name == "group.description" {
			field = "description"
		}
		if name == "group.username" {
			field = "username"
		}
		p[field] = textField(1024)
		add(name, "mutation", p, "peer", field)
	}
	p = peer()
	p["users"] = arrayField(1, 100, idField())
	add("group.add", "mutation", p, "peer", "users")
	for _, name := range []string{"group.permissions", "group.promote", "group.demote"} {
		p := peer()
		p["user_id"] = idField()
		req := []string{"peer", "user_id"}
		mode := "mutation"
		if name == "group.permissions" {
			mode = "read"
		}
		if name == "group.promote" {
			p["access_list"] = arrayField(0, len(allowedAccess), enumField(allowedAccess...))
			p["mode"] = enumField("replace")
			req = append(req, "access_list", "mode")
		}
		add(name, mode, p, req...)
	}
	add("group.default_permissions", "read", peer(), "peer")
	p = peer()
	p["access_list"] = arrayField(0, len(allowedAccess), enumField(allowedAccess...))
	p["mode"] = enumField("replace")
	add("group.default_permissions.set", "mutation", p, "peer", "access_list", "mode")
	for _, name := range []string{"group.leave", "group.join_public", "group.link.revoke"} {
		add(name, "mutation", peer(), "peer")
	}
	for _, name := range []string{"group.join", "group.preview"} {
		mode := "read"
		if name == "group.join" {
			mode = "mutation"
		}
		add(name, mode, map[string]domains.FieldSchema{"token": {Type: "string", MinLength: 1, MaxLength: 256, Pattern: "^[A-Za-z0-9_-]+$"}, "peer_type": enumField("group", "channel")}, "token", "peer_type")
	}
	p = peer()
	p["visibility"] = enumField("Visible", "Hidden")
	add("group.history", "mutation", p, "peer", "visibility")
	for _, name := range []string{"folders.add", "folders.edit"} {
		p := map[string]domains.FieldSchema{"name": text(128), "include_peers": arrayField(0, 100, peerField()), "exclude_peers": arrayField(0, 100, peerField())}
		req := []string{"name", "include_peers", "exclude_peers"}
		if name == "folders.edit" {
			p["folder_id"] = idField()
			req = append(req, "folder_id")
		}
		add(name, "mutation", p, req...)
	}
	add("folders.remove", "mutation", map[string]domains.FieldSchema{"folder_id": idField()}, "folder_id")
	for _, name := range []string{"poll.results", "poll.vote", "poll.voters"} {
		p := map[string]domains.FieldSchema{"poll_id": idField()}
		req := []string{"poll_id"}
		mode := "read"
		if name != "poll.results" {
			p["selection_index"] = boundedInt(-1, 99)
			req = append(req, "selection_index")
		}
		if name == "poll.vote" {
			mode = "mutation"
		}
		if name == "poll.voters" {
			p["start_id"] = textField(256)
		}
		add(name, mode, p, req...)
	}
	p = peer()
	p["question"] = text(1024)
	p["options"] = arrayField(2, 10, text(256))
	p["is_anonymous"] = domains.FieldSchema{Type: "boolean"}
	p["allows_multiple_answers"] = domains.FieldSchema{Type: "boolean"}
	add("send.poll", "mutation", p, "peer", "question", "options", "is_anonymous", "allows_multiple_answers")
	for _, name := range []string{"sticker.get", "sticker.pack.add", "sticker.pack.remove"} {
		mode := "mutation"
		if name == "sticker.get" {
			mode = "read"
		}
		add(name, mode, map[string]domains.FieldSchema{"sticker_set_id": idField()}, "sticker_set_id")
	}
	p = peer()
	p["sticker_set_id"] = idField()
	p["sticker_id"] = idField()
	add("send.sticker", "mutation", p, "peer", "sticker_set_id", "sticker_id")
	add("gif.list", "read", map[string]domains.FieldSchema{})
	p = peer()
	p["file_id"] = idField()
	add("send.gif", "mutation", p, "peer", "file_id")
	p = peer()
	p["latitude"] = domains.FieldSchema{Type: "number"}
	p["longitude"] = domains.FieldSchema{Type: "number"}
	add("send.location", "mutation", p, "peer", "latitude", "longitude")
	p = peer()
	p["first_name"] = text(256)
	p["last_name"] = textField(256)
	p["phone"] = domains.FieldSchema{Type: "string", MinLength: 6, MaxLength: 16, Pattern: `^\+?[1-9][0-9]{5,14}$`}
	p["user_id"] = idField()
	add("send.contact", "mutation", p, "peer", "first_name", "phone")
	return append(out, communicationOperations()...)
}
func normalizeExtended(name string, raw json.RawMessage) error {
	var p extendedRequest
	if json.Unmarshal(raw, &p) != nil {
		return domains.E("INVALID_REQUEST", "invalid operation payload", 400)
	}
	c := Contract{}
	seen := map[string]bool{}
	for _, id := range p.Users {
		if !c.ValidateUserID(id) || seen[id] {
			return domains.E("INVALID_USER_ID", "members must be unique Rubika user GUIDs", 400)
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, id := range p.MessageIDs {
		if !validMessageID(id) || seen[id] {
			return domains.E("INVALID_MESSAGE_ID", "message IDs must be unique positive decimal strings", 400)
		}
		seen[id] = true
	}
	for _, peers := range [][]domains.Peer{p.Include, p.Exclude} {
		seen := map[string]bool{}
		for _, v := range peers {
			if c.ValidatePeer(v) != nil || seen[v.Key()] {
				return domains.E("INVALID_PEER", "folder peers must be valid and unique", 400)
			}
			seen[v.Key()] = true
		}
	}
	seen = map[string]bool{}
	for _, v := range p.Access {
		if seen[v] {
			return domains.E("INVALID_REQUEST", "access list must contain unique permissions", 400)
		}
		seen[v] = true
	}
	for _, id := range []string{p.PollID, p.FolderID, p.StickerID, p.StickerSetID, p.FileID} {
		if id != "" && !domains.ValidOpaqueID(id) {
			return domains.E("INVALID_REQUEST", "identifier must be a bounded string without controls", 400)
		}
	}
	if name == "send.location" && (math.IsNaN(p.Latitude) || math.IsNaN(p.Longitude) || p.Latitude < -90 || p.Latitude > 90 || p.Longitude < -180 || p.Longitude > 180) {
		return domains.E("INVALID_REQUEST", "latitude and longitude are out of range", 400)
	}
	if name == "poll.voters" && p.Selection < 0 {
		return domains.E("INVALID_REQUEST", "voter selection index must be nonnegative", 400)
	}
	if (name == "group.join_public" || name == "group.username") && p.Peer.Type != "channel" {
		return domains.Unsupported("public join and username changes require a Rubika channel")
	}
	if (name == "group.default_permissions" || name == "group.default_permissions.set" || name == "group.history") && p.Peer.Type != "group" {
		return domains.Unsupported("operation requires a Rubika group")
	}
	for _, s := range p.Options {
		if strings.TrimSpace(s) == "" {
			return domains.E("INVALID_REQUEST", "poll options must not be empty", 400)
		}
	}
	return normalizeCommunication(name, raw)
}
