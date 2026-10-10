package rubikameow

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

type extendedRequest struct {
	Peer         domains.Peer   `json:"peer"`
	UserID       string         `json:"user_id"`
	Users        []string       `json:"users"`
	Phone        string         `json:"phone"`
	FirstName    string         `json:"first_name"`
	LastName     string         `json:"last_name"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	About        string         `json:"about"`
	Username     string         `json:"username"`
	Query        string         `json:"query"`
	SearchType   string         `json:"search_type"`
	Activity     string         `json:"activity"`
	MessageIDs   []string       `json:"message_ids"`
	ChannelType  string         `json:"channel_type"`
	Access       []string       `json:"access_list"`
	Mode         string         `json:"mode"`
	Token        string         `json:"token"`
	PeerType     string         `json:"peer_type"`
	Visibility   string         `json:"visibility"`
	Name         string         `json:"name"`
	Include      []domains.Peer `json:"include_peers"`
	Exclude      []domains.Peer `json:"exclude_peers"`
	FolderID     string         `json:"folder_id"`
	PollID       string         `json:"poll_id"`
	Selection    int            `json:"selection_index"`
	StartID      string         `json:"start_id"`
	Question     string         `json:"question"`
	Options      []string       `json:"options"`
	Anonymous    bool           `json:"is_anonymous"`
	Multiple     bool           `json:"allows_multiple_answers"`
	StickerID    string         `json:"sticker_id"`
	StickerSetID string         `json:"sticker_set_id"`
	FileID       string         `json:"file_id"`
	Latitude     float64        `json:"latitude"`
	Longitude    float64        `json:"longitude"`
}

func (c *Client) extendedCall(ctx context.Context, name string, raw json.RawMessage, rid string) (json.RawMessage, bool, error) {
	if name == "avatar.list" {
		result, err := c.listAvatars(ctx, raw)
		return result, true, err
	}
	if result, handled, err := c.avatarMutation(ctx, name, raw, rid); handled {
		return result, true, err
	}
	if result, handled, err := c.communicationCall(ctx, name, raw, rid); handled {
		return result, true, err
	}
	known, mutating := false, false
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
	finish := func(o object, err error) (json.RawMessage, bool, error) {
		if err != nil {
			return nil, true, err
		}
		b, err := json.Marshal(publicExtended(o))
		return b, true, err
	}
	var p extendedRequest
	_ = json.Unmarshal(raw, &p)
	method := ""
	params := object{}
	send := false
	prefix, key := "Group", "group_guid"
	if p.Peer.Type == "channel" {
		prefix, key = "Channel", "channel_guid"
	}
	switch name {
	case "contacts.add":
		method = "addAddressBook"
		params = object{"phone": p.Phone, "first_name": p.FirstName, "last_name": p.LastName}
	case "contacts.remove":
		method = "deleteContact"
		params = object{"user_guid": p.UserID}
	case "contacts.resolve":
		method = "getObjectByUsername"
		params = object{"username": p.Username}
	case "search.global":
		method = "searchGlobalObjects"
		params = object{"search_text": p.Query}
	case "chat.search":
		method = "searchChatMessages"
		params = object{"object_guid": p.Peer.ID, "search_text": p.Query, "type": p.SearchType}
	case "chat.get_by_ids":
		method = "getMessagesByID"
		params = object{"object_guid": p.Peer.ID, "message_ids": p.MessageIDs}
	case "chat.activity":
		method = "sendChatActivity"
		params = object{"object_guid": p.Peer.ID, "activity": p.Activity}
	case "account.name":
		method = "updateProfile"
		params = object{"first_name": p.FirstName, "last_name": p.LastName, "updated_parameters": []string{"first_name", "last_name"}}
	case "account.about":
		method = "updateProfile"
		params = object{"bio": p.About, "updated_parameters": []string{"bio"}}
	case "account.username":
		method = "updateUsername"
		params = object{"username": p.Username}
	case "group.create":
		method = "addGroup"
		params = object{"title": p.Title, "member_guids": p.Users}
	case "channel.create":
		method = "addChannel"
		params = object{"title": p.Title, "channel_type": p.ChannelType, "member_guids": p.Users}
		if p.Description != "" {
			params["description"] = p.Description
		}
	case "group.name", "group.description", "group.history":
		method = "edit" + prefix + "Info"
		field, value := "title", p.Title
		if name == "group.description" {
			field, value = "description", p.Description
		}
		if name == "group.history" {
			field, value = "chat_history_for_new_members", p.Visibility
		}
		params = object{key: p.Peer.ID, field: value, "updated_parameters": []string{field}}
	case "group.username":
		method = "updateChannelUsername"
		params = object{"channel_guid": p.Peer.ID, "username": p.Username}
	case "group.add":
		method = "add" + prefix + "Members"
		params = object{key: p.Peer.ID, "member_guids": p.Users}
	case "group.promote", "group.demote":
		method = "set" + prefix + "Admin"
		params = object{key: p.Peer.ID, "member_guid": p.UserID, "action": "UnsetAdmin"}
		if name == "group.promote" {
			params["action"] = "SetAdmin"
			params["access_list"] = p.Access
		}
	case "group.permissions":
		method = "get" + prefix + "AdminAccessList"
		params = object{key: p.Peer.ID, "member_guid": p.UserID}
	case "group.default_permissions":
		method = "getGroupDefaultAccess"
		params = object{"group_guid": p.Peer.ID}
	case "group.default_permissions.set":
		method = "setGroupDefaultAccess"
		params = object{"group_guid": p.Peer.ID, "access_list": p.Access}
	case "group.leave", "group.join_public":
		method = "leaveGroup"
		params = object{key: p.Peer.ID}
		if p.Peer.Type == "channel" {
			method = "joinChannelAction"
			params["action"] = "Leave"
			if name == "group.join_public" {
				params["action"] = "Join"
			}
		}
	case "group.link.revoke":
		method = "set" + prefix + "Link"
		params = object{key: p.Peer.ID}
	case "group.join", "group.preview":
		method = "joinGroup"
		if p.PeerType == "channel" {
			method = "joinChannelByLink"
		}
		if name == "group.preview" {
			method = p.PeerType + "PreviewByJoinLink"
		}
		params = object{"hash_link": p.Token}
	case "folders.add", "folders.edit":
		method = "addFolder"
		inc, exc := []string{}, []string{}
		for _, v := range p.Include {
			inc = append(inc, v.ID)
		}
		for _, v := range p.Exclude {
			exc = append(exc, v.ID)
		}
		params = object{"name": p.Name, "include_object_guids": inc, "exclude_object_guids": exc}
		if name == "folders.add" {
			params["include_chat_types"] = []string{}
			params["exclude_chat_types"] = []string{}
			params["is_add_to_top"] = true
		} else {
			method = "editFolder"
			params["folder_id"] = p.FolderID
			params["updated_parameters"] = []string{"name", "include_object_guids", "exclude_object_guids"}
		}
	case "folders.remove":
		method = "deleteFolder"
		params = object{"folder_id": p.FolderID}
	case "poll.results", "poll.vote", "poll.voters":
		method = "getPollStatus"
		params = object{"poll_id": p.PollID}
		if name == "poll.vote" {
			method = "votePoll"
			params["selection_index"] = p.Selection
		}
		if name == "poll.voters" {
			method = "getPollOptionVoters"
			params["selection_index"] = p.Selection
			if p.StartID != "" {
				params["start_id"] = p.StartID
			}
		}
	case "send.poll":
		method = "createPoll"
		send = true
		params = object{"object_guid": p.Peer.ID, "rnd": rid, "options": p.Options, "question": p.Question, "type": "Regular", "is_anonymous": p.Anonymous, "allows_multiple_answers": p.Multiple}
	case "sticker.get":
		method = "getStickersBySetIDs"
		params = object{"sticker_set_ids": []string{p.StickerSetID}}
	case "sticker.pack.add", "sticker.pack.remove":
		method = "actionOnStickerSet"
		action := "Add"
		if name == "sticker.pack.remove" {
			action = "Remove"
		}
		params = object{"sticker_set_id": p.StickerSetID, "action": action}
	case "gif.list":
		method = "getMyGifSet"
	case "send.sticker", "send.gif":
		lookup, lookupParams, list, idKey, want := "getStickersBySetIDs", object{"sticker_set_ids": []string{p.StickerSetID}}, "stickers", "sticker_id", p.StickerID
		if name == "send.gif" {
			lookup, lookupParams, list, idKey, want = "getMyGifSet", object{}, "gifs", "file_id", p.FileID
		}
		response, err := c.invoke(ctx, lookup, lookupParams, false, "")
		if err != nil {
			return finish(nil, err)
		}
		var selected object
		for _, item := range asObjects(response[list]) {
			if item.str(idKey) == want && (name != "send.sticker" || item.str("sticker_set_id") == p.StickerSetID) {
				selected = item
				break
			}
		}
		if selected == nil {
			return finish(nil, domains.E("MEDIA_NOT_FOUND", "selected provider asset is not available to this account", 404))
		}
		method = "sendMessage"
		send = true
		params = object{"object_guid": p.Peer.ID, "rnd": rid, "type": "Sticker", "sticker": selected}
		if name == "send.gif" {
			params = object{"object_guid": p.Peer.ID, "rnd": rid, "type": "FileInline", "file_inline": selected}
		}
	case "send.location":
		method = "sendMessage"
		send = true
		params = object{"object_guid": p.Peer.ID, "rnd": rid, "type": "Location", "location": object{"latitude": p.Latitude, "longitude": p.Longitude}}
	case "send.contact":
		method = "sendMessage"
		send = true
		contact := object{"first_name": p.FirstName, "last_name": p.LastName, "phone_number": p.Phone}
		if p.UserID != "" {
			contact["user_guid"] = p.UserID
		}
		params = object{"object_guid": p.Peer.ID, "rnd": rid, "type": "ContactMessage", "message_contact": contact}
	default:
		return finish(nil, domains.Unsupported(name))
	}
	response, err := c.invoke(ctx, method, params, mutating, "")
	if err != nil {
		return finish(nil, err)
	}
	if send {
		result, err := sendResult(response, p.Peer)
		if err != nil {
			return finish(nil, err)
		}
		raw, err := json.Marshal(result)
		return raw, true, err
	}
	if name == "chat.search" {
		ids, ok := response["message_ids"].([]any)
		if !ok || len(ids) > 1000 {
			return finish(nil, protocolError())
		}
		out := []string{}
		for _, value := range ids {
			id, ok := value.(string)
			if !ok || !validMessageID(id) {
				return finish(nil, protocolError())
			}
			out = append(out, id)
		}
		raw, err := json.Marshal(object{"message_ids": out, "complete": false})
		return raw, true, err
	}
	if name == "chat.get_by_ids" {
		items := []*domains.Message{}
		wanted := map[string]bool{}
		for _, id := range p.MessageIDs {
			wanted[id] = true
		}
		for _, message := range asObjects(response["messages"]) {
			id := message.str("message_id")
			if !wanted[id] || (message.str("object_guid") != "" && message.str("object_guid") != p.Peer.ID) {
				return finish(nil, protocolError())
			}
			delete(wanted, id)
			event, err := c.projectMessage(p.Peer.ID, object{"action": "New", "object_guid": p.Peer.ID, "message_id": id, "message": message})
			if err != nil {
				return finish(nil, err)
			}
			if event.Media != nil && c.cfg.SaveMediaReference != nil {
				saved, err := c.cfg.SaveMediaReference(ctx, event.Peer, event.MessageID, *event.Media)
				if err != nil {
					return finish(nil, err)
				}
				event.Message.Media.DownloadSupported = saved
			}
			items = append(items, event.Message)
		}
		raw, err := json.Marshal(object{"items": items})
		return raw, true, err
	}
	if status := response.str("status"); status != "" && status != "OK" && status != "Done" {
		return finish(nil, rpcError(status))
	}
	if mutating {
		response["acknowledged"] = true
	}
	return finish(response, nil)
}

func publicExtended(o object) object {
	out := publicObject(o)
	for _, key := range []string{"poll_id", "sticker_id", "sticker_set_id", "folder_id", "file_id", "message_id", "avatar_id"} {
		if v := o.str(key); v != "" {
			out[key] = v
		} else if n, ok := o[key].(json.Number); ok {
			out[key] = n.String()
		}
	}
	for _, key := range []string{"name", "bio", "question", "phone_number", "emoji_character", "username", "type", "join_link", "next_start_id", "start_id"} {
		if v, ok := o[key].(string); ok {
			out[key] = v
		}
	}
	for _, key := range []string{"acknowledged", "has_continue", "is_anonymous", "allows_multiple_answers", "is_closed", "is_voted", "is_active"} {
		if v, ok := o[key].(bool); ok {
			out[key] = v
		}
	}
	for _, key := range []string{"selection_index", "total_vote", "count_vote", "count_members", "latitude", "longitude", "width", "height"} {
		if v, ok := o[key]; ok {
			switch v.(type) {
			case json.Number, float64, int, int64:
				out[key] = v
			}
		}
	}
	for _, key := range []string{"access_list", "options", "percent_vote_options", "count_vote_options", "include_object_guids", "exclude_object_guids"} {
		if a, ok := o[key].([]any); ok {
			v := []any{}
			for _, x := range a {
				switch x.(type) {
				case string, json.Number, bool, float64, int, int64:
					v = append(v, x)
				}
			}
			out[key] = v
		}
	}
	for _, key := range []string{"user", "group", "channel", "folder", "poll", "poll_status", "abs_object", "location", "message_contact"} {
		if item := asObject(o[key]); item != nil {
			out[key] = publicExtended(item)
		}
	}
	for _, key := range []string{"users", "groups", "channels", "objects", "folders", "stickers", "gifs", "voters_abs_objects"} {
		if _, ok := o[key]; !ok {
			continue
		}
		rows := []object{}
		for _, item := range asObjects(o[key]) {
			rows = append(rows, publicExtended(item))
		}
		out[key] = rows
	}
	return out
}

func projectExtendedMessage(message object, msg *domains.Message) (object, bool, error) {
	// sendMessage accepts message_contact, but received ContactMessage records
	// use contact_message. Keep the observed receive layout distinct from writes.
	if raw, present := message["contact_message"]; present {
		contact := asObject(raw)
		if contact == nil {
			return nil, true, protocolError()
		}
		out := object{}
		for _, key := range []string{"first_name", "last_name", "phone_number"} {
			if value, present := contact[key]; present {
				text, ok := value.(string)
				if !ok || len(text) > 1024 {
					return nil, true, protocolError()
				}
				out[key] = text
			}
		}
		if value, present := contact["user_guid"]; present {
			guid, ok := value.(string)
			if !ok || (guid != "" && !(Contract{}).ValidateUserID(guid)) {
				return nil, true, protocolError()
			}
			if guid != "" {
				out["user_guid"] = guid
			}
		}
		msg.Kind, msg.Supported = "contact", true
		return object{"contact_message": out}, true, nil
	}
	for _, entry := range []struct{ key, kind string }{{"poll", "poll"}, {"location", "location"}, {"sticker", "sticker"}} {
		item := asObject(message[entry.key])
		if item == nil {
			continue
		}
		if entry.kind == "poll" && !domains.ValidOpaqueID(item.str("poll_id")) {
			return nil, true, protocolError()
		}
		if entry.kind == "sticker" && (!domains.ValidOpaqueID(item.str("sticker_id")) || !domains.ValidOpaqueID(item.str("sticker_set_id"))) {
			return nil, true, protocolError()
		}
		msg.Kind, msg.Supported = entry.kind, true
		return object{entry.key: publicExtended(item)}, true, nil
	}
	if strings.EqualFold(message.str("type"), "ContactMessage") {
		msg.Kind = "unsupported"
		msg.Supported = false
	}
	return nil, false, nil
}
