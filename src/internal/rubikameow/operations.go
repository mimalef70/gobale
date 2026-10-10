package rubikameow

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

type callRequest struct {
	Peer       domains.Peer `json:"peer"`
	SourcePeer domains.Peer `json:"source_peer"`
	MessageID  string       `json:"message_id"`
	RequestID  string       `json:"request_id"`
	UserID     string       `json:"user_id"`
	Text       string       `json:"message"`
	StartID    string       `json:"start_id"`
	OffsetID   string       `json:"offset_id"`
	Limit      int          `json:"limit"`
	ReactionID string       `json:"reaction_id"`
	JustMine   *bool        `json:"just_mine"`
}

func (c *Client) Send(ctx context.Context, r domains.SendRequest) (domains.SendResult, error) {
	if e := (Contract{}).ValidateSend(r); e != nil {
		return domains.SendResult{}, e
	}
	if !validMessageID(r.RequestID) {
		return domains.SendResult{}, domains.E("INVALID_REQUEST_ID", "persist a positive request ID before sending", 400)
	}
	if r.MediaID != "" {
		return c.sendMedia(ctx, r)
	}
	input := object{"object_guid": r.Peer.ID, "rnd": r.RequestID, "text": r.Text}
	if r.ReplyMessageID != "" {
		input["reply_to_message_id"] = r.ReplyMessageID
	}
	o, e := c.invoke(ctx, "sendMessage", input, true, "")
	if e != nil {
		return domains.SendResult{}, e
	}
	return sendResult(o, r.Peer)
}
func sendResult(o object, peer domains.Peer) (domains.SendResult, error) {
	update := asObject(o["message_update"])
	if update == nil {
		updates := asObjects(o["message_updates"])
		if len(updates) == 1 {
			update = updates[0]
		}
	}
	m := asObject(update["message"])
	id := update.str("message_id")
	if id == "" {
		id = m.str("message_id")
	}
	if o.str("status") != "OK" || !validMessageID(id) || update.str("object_guid") != peer.ID {
		return domains.SendResult{}, &domains.Error{Code: "SEND_UNKNOWN", Message: "Rubika did not supply a verified send identity", HTTP: 502, Ambiguous: true}
	}
	result := domains.SendResult{MessageID: id}
	if date := m.num("time"); date > 0 {
		result.Date = time.Unix(date, 0).UTC()
	}
	return result, nil
}
func publicObject(o object) object {
	out := object{}
	for _, key := range []string{"user_guid", "group_guid", "channel_guid", "bot_guid", "service_guid", "object_guid", "title", "first_name", "last_name", "username", "description", "is_verified", "is_deleted", "count_members", "count_unseen", "last_message_id", "is_pinned", "is_archived", "last_seen_my_mid"} {
		if value, ok := o[key]; ok {
			if strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_guid") || key == "last_seen_my_mid" {
				if id := o.str(key); id != "" {
					out[key] = id
				}
				continue
			}
			switch v := value.(type) {
			case string, bool, json.Number, int64, int:
				out[key] = v
			}
		}
	}
	return out
}
func (c *Client) Call(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	fields, e := jsonObject(raw)
	if e != nil {
		return nil, domains.E("INVALID_REQUEST", "invalid operation payload", 400)
	}
	rid := fields.str("request_id")
	delete(fields, "request_id")
	public, _ := json.Marshal(fields)
	normalized, _, e := (Contract{}).NormalizeOperation(name, public)
	if e != nil {
		return nil, e
	}
	var p callRequest
	if json.Unmarshal(normalized, &p) != nil {
		return nil, protocolError()
	}
	mutation := false
	for _, op := range (Contract{}).Operations() {
		if op.Operation == name {
			mutation = op.Mode == "mutation"
		}
	}
	if mutation && !validMessageID(rid) {
		return nil, domains.E("INVALID_REQUEST_ID", "persist a positive request ID before mutation", 400)
	}
	if result, handled, err := c.extendedCall(ctx, name, normalized, rid); handled {
		return result, err
	}
	method := ""
	params := object{}
	projection := "acknowledged"
	key := ""
	channel := p.Peer.Type == "channel"
	objectKey := "group_guid"
	prefix := "Group"
	if channel {
		objectKey = "channel_guid"
		prefix = "Channel"
	}
	switch name {
	case "account.info":
		c.mu.RLock()
		user := c.session.UserID
		c.mu.RUnlock()
		method = "getUserInfo"
		params["user_guid"] = user
		projection = "one"
		key = "user"
	case "chat.info":
		prefix := map[string]string{"user": "User", "group": "Group", "channel": "Channel", "bot": "Bot", "service": "Service"}[p.Peer.Type]
		method = "get" + prefix + "Info"
		key = strings.ToLower(prefix)
		params[key+"_guid"] = p.Peer.ID
		projection = "one"
	case "users.get":
		if p.Peer.Type != "user" {
			return nil, domains.E("INVALID_PEER", "user peer is required", 400)
		}
		method = "getUserInfo"
		params["user_guid"] = p.Peer.ID
		projection = "one"
		key = "user"
	case "contacts.list":
		method = "getContacts"
		projection = "list"
		key = "users"
	case "chat.list", "group.list", "channel.list":
		method = "getChats"
		if p.StartID != "" {
			params["start_id"] = p.StartID
		}
		projection = "chats"
		key = "chats"
	case "chat.history", "chat.messages":
		return c.history(ctx, p)
	case "group.info":
		method = "get" + prefix + "Info"
		params[objectKey] = p.Peer.ID
		projection = "one"
		key = strings.ToLower(prefix)
	case "group.link":
		method = "get" + prefix + "Link"
		params[objectKey] = p.Peer.ID
		projection = "link"
	case "group.members":
		method = "get" + prefix + "AllMembers"
		params[objectKey] = p.Peer.ID
		if p.StartID != "" {
			params["start_id"] = p.StartID
		}
		projection = "members"
		key = "in_chat_members"
	case "group.admins":
		method = "get" + prefix + "AdminMembers"
		params[objectKey] = p.Peer.ID
		projection = "members"
		key = "in_chat_members"
	case "group.banned":
		method = "getBanned" + prefix + "Members"
		params[objectKey] = p.Peer.ID
		projection = "members"
		key = "in_chat_members"
	case "message.edit":
		method = "editMessage"
		params = object{"object_guid": p.Peer.ID, "message_id": p.MessageID, "text": p.Text}
	case "message.forward":
		method = "forwardMessages"
		params = object{"from_object_guid": p.SourcePeer.ID, "to_object_guid": p.Peer.ID, "message_ids": []string{p.MessageID}, "rnd": rid}
		projection = "send"
	case "message.delete":
		method = "deleteMessages"
		typ := "Global"
		if *p.JustMine {
			typ = "Local"
		}
		params = object{"object_guid": p.Peer.ID, "message_ids": []string{p.MessageID}, "type": typ}
	case "message.read":
		method = "seenChats"
		params = object{"seen_list": object{p.Peer.ID: p.MessageID}}
	case "message.pin", "message.unpin":
		method = "setPinMessage"
		action := "Pin"
		if name == "message.unpin" {
			action = "Unpin"
		}
		params = object{"object_guid": p.Peer.ID, "message_id": p.MessageID, "action": action}
	case "message.reaction", "message.reaction.remove":
		method = "actionOnMessageReaction"
		action := "Add"
		if name == "message.reaction.remove" {
			action = "Remove"
		}
		params = object{"object_guid": p.Peer.ID, "message_id": p.MessageID, "action": action}
		if action == "Add" {
			params["reaction_id"] = p.ReactionID
		}
	case "group.ban", "group.unban":
		method = "ban" + prefix + "Member"
		action := "Set"
		if name == "group.unban" {
			action = "Unset"
		}
		params = object{objectKey: p.Peer.ID, "member_guid": p.UserID, "action": action}
	case "contacts.block", "contacts.unblock":
		method = "setBlockUser"
		action := "Block"
		if name == "contacts.unblock" {
			action = "Unblock"
		}
		params = object{"user_guid": p.UserID, "action": action}
	case "folders.list":
		method = "getFolders"
		projection = "folders"
		key = "folders"
	case "sticker.list":
		method = "getMyStickerSets"
		projection = "stickers"
		key = "sticker_sets"
	default:
		return nil, domains.Unsupported(name)
	}
	o, e := c.invoke(ctx, method, params, mutation, "")
	if e != nil {
		return nil, e
	}
	switch projection {
	case "send":
		result, e := sendResult(o, p.Peer)
		if e != nil {
			return nil, e
		}
		return json.Marshal(result)
	case "acknowledged":
		if status := o.str("status"); status != "" && status != "OK" && status != "Done" {
			return nil, rpcError(status)
		}
		return json.Marshal(object{"acknowledged": true})
	case "one":
		row := asObject(o[key])
		if row == nil {
			return nil, protocolError()
		}
		if name == "chat.info" && row.str(key+"_guid") != p.Peer.ID {
			return nil, protocolError()
		}
		return json.Marshal(publicObject(row))
	case "link":
		return json.Marshal(object{"join_link": o.str("join_link")})
	case "members":
		rows, ok := o[key].([]any)
		if !ok || len(rows) > 1000 {
			return nil, protocolError()
		}
		items := []object{}
		seen := map[string]bool{}
		for _, value := range rows {
			row := asObject(value)
			id := row.str("member_guid")
			peer := peerFromGUID(id)
			if (Contract{}).ValidatePeer(peer) != nil || seen[id] {
				return nil, protocolError()
			}
			seen[id] = true
			item := publicObject(row)
			item["member_guid"] = id
			if kind := row.str("member_type"); kind != "" {
				if strings.ToLower(kind) != peer.Type {
					return nil, protocolError()
				}
				item["member_type"] = kind
			}
			items = append(items, item)
		}
		return json.Marshal(object{"items": items, "has_continue": o["has_continue"], "next_start_id": o.str("next_start_id")})
	case "folders":
		items := []object{}
		for _, row := range asObjects(o[key]) {
			items = append(items, object{"id": row.str("folder_id"), "name": row.str("name")})
		}
		return json.Marshal(object{"items": items})
	case "stickers":
		items := []object{}
		for _, row := range asObjects(o[key]) {
			items = append(items, object{"id": row.str("sticker_set_id"), "title": row.str("title")})
		}
		return json.Marshal(object{"items": items})
	default:
		items := []object{}
		for _, row := range asObjects(o[key]) {
			if projection == "chats" {
				guid := row.str("object_guid")
				if name == "group.list" && !strings.HasPrefix(guid, "g0") || name == "channel.list" && !strings.HasPrefix(guid, "c0") {
					continue
				}
			}
			items = append(items, publicObject(row))
		}
		return json.Marshal(object{"items": items, "has_continue": o["has_continue"], "next_start_id": o.str("next_start_id")})
	}
}
