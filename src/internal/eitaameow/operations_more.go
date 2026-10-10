package eitaameow

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func moreOperations() []domains.OperationContract {
	add := func(name string, properties map[string]domains.FieldSchema, required ...string) domains.OperationContract {
		return domains.OperationContract{Operation: name, Mode: "mutation", Path: "/operations/" + name, Method: "POST", Description: "Eitaa " + name, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: properties, Required: required}}
	}
	ops := []domains.OperationContract{
		add("poll.close", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField()}, "peer", "message_id"),
	}
	for _, name := range []string{"message.pin", "message.unpin"} {
		ops = append(ops, add(name, map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField(), "silent": {Type: "boolean"}, "just_mine": {Type: "boolean"}}, "peer", "message_id"))
	}
	ops = append(ops, add("chat.activity", map[string]domains.FieldSchema{"peer": peerField(), "activity": {Type: "string", Enum: []json.RawMessage{json.RawMessage(`"typing"`), json.RawMessage(`"cancel"`)}}}, "peer", "activity"))
	search := add("chat.search", map[string]domains.FieldSchema{"peer": peerField(), "query": {Type: "string", MinLength: 1, MaxLength: 512}, "cursor": textField(2048), "limit": boundedInt(1, 100)}, "peer", "query")
	search.Mode = "read"
	search.Schedulable = false
	ops = append(ops, search)
	rights := domains.FieldSchema{Type: "object", Properties: map[string]domains.FieldSchema{}, Required: permissionFields}
	for _, key := range permissionFields {
		rights.Properties[key] = domains.FieldSchema{Type: "boolean"}
	}
	for _, name := range []string{"group.permissions", "group.member.permissions"} {
		props := map[string]domains.FieldSchema{"peer": peerField(), "permissions": rights, "until": textField(64)}
		required := []string{"peer", "permissions"}
		if name == "group.member.permissions" {
			props["user"] = idField()
			required = append(required, "user")
		}
		ops = append(ops, add(name, props, required...))
	}
	for i := range ops {
		ops[i].Schedulable = false
	}
	return ops

}

var permissionFields = []string{"view_messages", "send_messages", "send_media", "send_stickers", "send_gifs", "send_games", "send_inline", "send_polls", "change_info", "embed_links", "view_participants", "invite_users", "pin_messages", "send_forwarded_messages"}

type moreRequest struct {
	Silent      bool            `json:"silent"`
	JustMine    bool            `json:"just_mine"`
	Activity    string          `json:"activity"`
	Query       string          `json:"query"`
	Cursor      string          `json:"cursor"`
	Limit       int             `json:"limit"`
	User        string          `json:"user"`
	Permissions map[string]bool `json:"permissions"`
	Until       string          `json:"until"`
	Peer        domains.Peer    `json:"peer"`
	MessageID   string          `json:"message_id"`
	DocumentID  string          `json:"document_id"`
	ShortName   string          `json:"short_name"`
	Reply       string          `json:"reply_to_message_id"`
}

func normalizeMore(name string, raw json.RawMessage) error {
	if name == "group.permissions" || name == "group.member.permissions" {
		var p moreRequest
		_ = json.Unmarshal(raw, &p)
		if p.Until != "" {
			date, e := time.Parse(time.RFC3339, p.Until)
			if e != nil || date.Unix() < 1 || date.Unix() > 2147483647 {
				return domains.E("INVALID_REQUEST", "until must be an RFC3339 time within the provider's int32 range", 400)
			}
		}
		return nil
	}
	return nil
}
func (c *Client) moreCall(ctx context.Context, name string, raw json.RawMessage, rid int64) (json.RawMessage, bool, error) {
	known := false
	for _, op := range moreOperations() {
		if op.Operation == name {
			known = true
		}
	}
	if !known {
		return nil, false, nil
	}
	var p moreRequest
	_ = json.Unmarshal(raw, &p)
	peer, e := c.inputPeer(ctx, p.Peer)
	if e != nil {
		return nil, true, e
	}
	if name == "chat.search" {
		out, e := c.providerSearch(ctx, p, peer)
		return out, true, e
	}
	if name == "message.pin" || name == "message.unpin" || name == "chat.activity" || strings.HasPrefix(name, "group.") {
		method := "messages.updatePinnedMessage"
		id, _ := messageNumber(p.MessageID)
		params := object{"peer": peer, "id": id, "unpin": name == "message.unpin", "silent": p.Silent, "pm_oneside": p.JustMine}
		if name == "chat.activity" {
			action := "sendMessageTypingAction"
			if p.Activity == "cancel" {
				action = "sendMessageCancelAction"
			}
			method = "messages.setTyping"
			params = object{"peer": peer, "action": object{"_": action}}
		}
		if strings.HasPrefix(name, "group.") {
			method = "messages.editChatDefaultBannedRights"
			rights := object{"_": "chatBannedRights", "until_date": 0}
			for key, value := range p.Permissions {
				rights[key] = value
			}
			if p.Until != "" {
				date, _ := time.Parse(time.RFC3339, p.Until)
				rights["until_date"] = date.Unix()
			}
			params = object{"peer": peer, "banned_rights": rights}
			if name == "group.member.permissions" {
				if peer.str("_") != "inputPeerChannel" {
					return nil, true, domains.Unsupported("member permissions require channel or supergroup")
				}
				target, e := c.inputPeer(ctx, domains.Peer{Type: "user", ID: p.User})
				if e != nil {
					return nil, true, e
				}
				method = "channels.editBanned"
				params = object{"channel": inputChannel(peer), "participant": target, "banned_rights": rights}
			}
		}
		response, e := c.invoke(ctx, method, params, true, false)
		if e != nil {
			return nil, true, e
		}
		if name == "chat.activity" {
			if response["acknowledged"] != true {
				return nil, true, domains.E("PROVIDER_REJECTED", "Eitaa did not accept chat activity", 502)
			}
		} else if !isUpdatesResponse(response) {
			return nil, true, &domains.Error{Code: "SEND_UNKNOWN", Message: "Eitaa did not acknowledge mutation", HTTP: 502, Ambiguous: true}
		}
		out, e := json.Marshal(object{"acknowledged": true})
		return out, true, e
	}
	if name == "poll.close" {
		out, e := c.closePoll(ctx, p, peer)
		return out, true, e
	}
	return nil, true, domains.Unsupported(name)
}
func (c *Client) closePoll(ctx context.Context, p moreRequest, peer object) (json.RawMessage, error) {
	id, _ := messageNumber(p.MessageID)
	method := "messages.getMessages"
	params := object{"id": []object{{"_": "inputMessageID", "id": id}}}
	if peer.str("_") == "inputPeerChannel" {
		method = "channels.getMessages"
		params["channel"] = inputChannel(peer)
	}
	response, e := c.invoke(ctx, method, params, false, false)
	if e != nil {
		return nil, e
	}
	var found object
	for _, m := range asObjects(response["messages"]) {
		if m.str("_") != "message" || m.num("id") != id {
			continue
		}
		actual, e := c.messagePeer(m)
		if e != nil || actual.ID != p.Peer.ID || actual.Type != p.Peer.Type {
			continue
		}
		if found != nil {
			return nil, protocolError()
		}
		found = m
	}
	if found == nil {
		return nil, domains.E("MESSAGE_NOT_FOUND", "poll message was not found in the selected conversation", 404)
	}
	media := asObject(found["media"])
	original := asObject(media["poll"])
	if media.str("_") != "messageMediaPoll" || original.str("_") != "poll" {
		return nil, domains.E("INVALID_REQUEST", "selected message is not a poll", 400)
	}
	// Preserve all fields reviewed in this layer, including answer option bytes,
	// quiz/public/multiple flags and any provider close dates. Only closed changes.
	poll := object{}
	for key, value := range original {
		poll[key] = value
	}
	poll["closed"] = true
	result, e := c.invoke(ctx, "messages.editMessage", object{"peer": peer, "id": id, "media": object{"_": "inputMediaPoll", "poll": poll}}, true, false)
	if e != nil {
		return nil, e
	}
	if result.str("_") != "updates" && result.str("_") != "updatesCombined" && result.str("_") != "updateShort" {
		return nil, &domains.Error{Code: "SEND_UNKNOWN", Message: "Eitaa did not acknowledge poll closure", HTTP: 502, Ambiguous: true}
	}
	return json.Marshal(object{"acknowledged": true})
}

func isUpdatesResponse(o object) bool {
	return o.str("_") == "updates" || o.str("_") == "updatesCombined" || o.str("_") == "updateShort"
}
func (c *Client) providerSearch(ctx context.Context, p moreRequest, peer object) (json.RawMessage, error) {
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	kind := "search:" + eventHash(p.Query)
	cur, e := c.decodeCursor(p.Cursor, kind, p.Peer.Key())
	if e != nil {
		return nil, e
	}
	offset := cur.ID
	response, e := c.invoke(ctx, "messages.search", object{"peer": peer, "q": p.Query, "filter": object{"_": "inputMessagesFilterEmpty"}, "min_date": 0, "max_date": 0, "offset_id": offset, "add_offset": 0, "limit": limit, "max_id": 0, "min_id": 0, "hash": 0}, false, false)
	if e != nil {
		return nil, e
	}
	if e = c.rememberEntities(ctx, response); e != nil {
		return nil, e
	}
	items := []*domains.Message{}
	next := int64(0)
	nextDate := int64(0)
	seen := map[string]bool{}
	for _, message := range asObjects(response["messages"]) {
		event, e := c.projectReadEvent(ctx, message, p.Peer)
		if e != nil {
			return nil, e
		}
		if event.Peer.Key() != p.Peer.Key() {
			return nil, domains.E("PROVIDER_PROTOCOL_ERROR", "Eitaa search returned a different conversation", 502)
		}
		id, _ := messageNumber(event.MessageID)
		if offset > 0 && id >= offset {
			return nil, domains.E("NON_ADVANCING_CURSOR", "Eitaa search cursor did not advance", 502)
		}
		if seen[event.MessageID] {
			continue
		}
		seen[event.MessageID] = true
		if next == 0 || id < next {
			next = id
			nextDate = message.num("date")
		}
		items = append(items, event.Message)
	}
	if len(items) > limit {
		return nil, protocolError()
	}
	result := object{"items": items, "has_more": false, "complete": false}
	if len(items) == limit && next > 0 {
		result["has_more"] = true
		result["next_cursor"] = c.encodeCursor(pageCursor{Kind: kind, Peer: p.Peer.Key(), ID: next, Date: nextDate, OffsetPeer: p.Peer})
	}
	return json.Marshal(result)
}
