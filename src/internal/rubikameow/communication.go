package rubikameow

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

type communicationRequest struct {
	Peer          domains.Peer   `json:"peer"`
	Peers         []domains.Peer `json:"peers"`
	Users         []string       `json:"users"`
	UserID        string         `json:"user_id"`
	Query         string         `json:"query"`
	SearchType    string         `json:"search_type"`
	StartID       string         `json:"start_id"`
	Action        string         `json:"action"`
	Duration      *int64         `json:"duration_seconds"`
	LastMessageID string         `json:"last_message_id"`
	AppURL        string         `json:"app_url"`
	MediaID       string         `json:"media_id"`
	AvatarID      string         `json:"avatar_id"`
}

func communicationOperations() []domains.OperationContract {
	var out []domains.OperationContract
	add := func(name, mode, description string, fields map[string]domains.FieldSchema, required ...string) {
		out = append(out, domains.OperationContract{Operation: name, Method: "POST", Path: "/operations/" + name, Mode: mode, Description: description, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: fields, Required: required}})
	}
	peer := func() map[string]domains.FieldSchema { return map[string]domains.FieldSchema{"peer": peerField()} }
	add("contacts.online", "read", "Read selected contacts' provider presence metadata", map[string]domains.FieldSchema{"users": arrayField(1, 50, idField())}, "users")
	add("peers.get", "read", "Read selected peers through the authenticated account", map[string]domains.FieldSchema{"peers": arrayField(1, 50, peerField())}, "peers")
	add("search.messages", "read", "Search provider messages with explicit peer and projected content; results are not an exhaustive transcript", map[string]domains.FieldSchema{"query": {Type: "string", MinLength: 1, MaxLength: 512}, "search_type": enumField("Text", "Hashtag"), "start_id": textField(256)}, "query", "search_type")
	add("group.common", "read", "Read groups shared with a user", map[string]domains.FieldSchema{"user_id": idField()}, "user_id")
	add("group.online", "read", "Read the provider's group online count", peer(), "peer")
	p := peer()
	p["query"] = textField(256)
	add("group.mentions", "read", "Read mention candidates from the selected group", p, "peer")
	p = peer()
	p["action"] = enumField("Mute", "Unmute", "Pin", "Unpin", "Block", "Unblock", "Archive", "UnArchive")
	p["duration_seconds"] = boundedInt(1, 31536000)
	add("chat.action", "mutation", "Apply one explicit chat setting; duration applies only to Mute", p, "peer", "action")
	p = peer()
	p["last_message_id"] = idField()
	add("chat.history.clear", "mutation", "Explicitly clear provider history through the supplied boundary; local audit history is retained", p, "peer", "last_message_id")
	p = peer()
	p["last_message_id"] = idField()
	add("chat.remove", "mutation", "Explicitly remove a provider user, bot or service chat through the supplied boundary; local audit history is retained", p, "peer", "last_message_id")
	add("group.chat.remove", "mutation", "Remove the provider's no-access group chat entry; local audit history is retained", peer(), "peer")
	add("link.resolve", "read", "Resolve a public Rubika communication link without fetching its destination", map[string]domains.FieldSchema{"app_url": {Type: "string", MinLength: 1, MaxLength: 2048}}, "app_url")
	add("account.avatar", "mutation", "Set the authenticated account photo from owned media", map[string]domains.FieldSchema{"media_id": idField()}, "media_id")
	add("avatar.list", "read", "List opaque avatar IDs without private download references", peer(), "peer")
	p = peer()
	p["media_id"] = idField()
	add("group.photo", "mutation", "Set a group or channel photo from owned media", p, "peer", "media_id")
	p = peer()
	p["avatar_id"] = idField()
	add("avatar.remove", "mutation", "Remove an avatar after fresh account-scoped reference verification", p, "peer", "avatar_id")
	return out
}
func normalizeCommunication(name string, raw json.RawMessage) error {
	var p communicationRequest
	if json.Unmarshal(raw, &p) != nil {
		return domains.E("INVALID_REQUEST", "invalid operation payload", 400)
	}
	seen := map[string]bool{}
	for _, id := range p.Users {
		if !(Contract{}).ValidateUserID(id) || seen[id] {
			return domains.E("INVALID_USER_ID", "users must be unique Rubika user GUIDs", 400)
		}
		seen[id] = true
	}
	for _, peer := range p.Peers {
		if (Contract{}).ValidatePeer(peer) != nil || seen[peer.ID] {
			return domains.E("INVALID_PEER", "peers must be valid and unique", 400)
		}
		seen[peer.ID] = true
	}
	switch name {
	case "group.online", "group.mentions", "group.chat.remove":
		if p.Peer.Type != "group" {
			return domains.E("INVALID_PEER", "operation requires a Rubika group", 400)
		}
	case "chat.remove":
		if p.Peer.Type != "user" && p.Peer.Type != "bot" && p.Peer.Type != "service" {
			return domains.E("INVALID_PEER", "chat removal requires a user, bot or service peer", 400)
		}
		if p.LastMessageID != "0" && !validMessageID(p.LastMessageID) {
			return domains.E("INVALID_MESSAGE_ID", "explicit history boundary is required", 400)
		}
	case "chat.history.clear":
		if !validMessageID(p.LastMessageID) {
			return domains.E("INVALID_MESSAGE_ID", "explicit positive history boundary is required", 400)
		}
	case "chat.action":
		if p.Duration != nil && p.Action != "Mute" {
			return domains.E("INVALID_REQUEST", "duration is valid only for Mute", 400)
		}
		if (p.Action == "Block" || p.Action == "Unblock") && p.Peer.Type != "user" {
			return domains.E("INVALID_PEER", "blocking requires a user peer", 400)
		}
	case "link.resolve":
		if !publicRubikaLink(p.AppURL, false) {
			return domains.E("INVALID_REQUEST", "a public HTTPS Rubika link without credentials, query or fragment is required", 400)
		}
	case "account.avatar", "group.photo":
		if !domains.ValidOpaqueID(p.MediaID) {
			return domains.E("INVALID_MEDIA", "owned media ID is required", 400)
		}
	case "avatar.remove":
		if !domains.ValidOpaqueID(p.AvatarID) {
			return domains.E("INVALID_REQUEST", "a bounded avatar ID is required", 400)
		}
	}
	if p.StartID != "" && !domains.ValidOpaqueID(p.StartID) {
		return domains.E("INVALID_REQUEST", "invalid search cursor", 400)
	}
	return nil
}
func publicRubikaLink(raw string, resolved bool) bool {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || u.Path == "" || u.Path == "/" {
		return false
	}
	if u.Scheme == "https" && u.Host == "rubika.ir" {
		return true
	}
	if !resolved || u.Scheme != "rubika" {
		return false
	}
	switch u.Host {
	case "g.rubika.ir", "c.rubika.ir", "s.rubika.ir", "l.rubika.ir", "r.rubika.ir", "b.rubika.ir":
		return true
	}
	return false
}
func (c *Client) communicationCall(ctx context.Context, name string, raw json.RawMessage, rid string) (json.RawMessage, bool, error) {
	var p communicationRequest
	_ = json.Unmarshal(raw, &p)
	method := ""
	params := object{}
	mutating := false
	switch name {
	case "contacts.online":
		method = "getContactsLastOnline"
		params = object{"user_guids": p.Users}
	case "peers.get":
		method = "getAbsObjects"
		ids := []string{}
		for _, v := range p.Peers {
			ids = append(ids, v.ID)
		}
		params = object{"objects_guids": ids}
	case "search.messages":
		method = "searchGlobalMessages"
		params = object{"search_text": p.Query, "type": p.SearchType}
		if p.StartID != "" {
			params["start_id"] = p.StartID
		}
	case "group.common":
		method = "getCommonGroups"
		params = object{"user_guid": p.UserID}
	case "group.online":
		method = "getGroupOnlineCount"
		params = object{"group_guid": p.Peer.ID}
	case "group.mentions":
		method = "getGroupMentionList"
		params = object{"group_guid": p.Peer.ID, "search_mention": nil}
		if p.Query != "" {
			params["search_mention"] = p.Query
		}
	case "chat.action":
		method = "setActionChat"
		mutating = true
		params = object{"object_guid": p.Peer.ID, "action": p.Action}
		if p.Duration != nil {
			params["duration"] = *p.Duration
		}
	case "chat.history.clear":
		method = "deleteChatHistory"
		mutating = true
		params = object{"object_guid": p.Peer.ID, "last_message_id": p.LastMessageID}
	case "chat.remove":
		prefix := map[string]string{"user": "User", "bot": "Bot", "service": "Service"}[p.Peer.Type]
		method = "delete" + prefix + "Chat"
		mutating = true
		params = object{p.Peer.Type + "_guid": p.Peer.ID, "last_deleted_message_id": p.LastMessageID}
	case "group.chat.remove":
		method = "deleteNoAccessGroupChat"
		mutating = true
		params = object{"group_guid": p.Peer.ID}
	case "link.resolve":
		method = "getLinkFromAppUrl"
		params = object{"app_url": p.AppURL}
	default:
		return nil, false, nil
	}
	response, err := c.invoke(ctx, method, params, mutating, "")
	if err != nil {
		return nil, true, err
	}
	out, err := c.projectCommunication(ctx, name, p, response)
	if err != nil {
		return nil, true, err
	}
	result, err := json.Marshal(out)
	return result, true, err
}
func (c *Client) projectCommunication(ctx context.Context, name string, p communicationRequest, response object) (object, error) {
	switch name {
	case "contacts.online", "peers.get", "group.common", "group.mentions":
		field := "users"
		identity := "user_guid"
		wanted := map[string]bool{}
		for _, id := range p.Users {
			wanted[id] = true
		}
		if name == "peers.get" {
			field = "abs_objects"
			identity = "object_guid"
			for _, v := range p.Peers {
				wanted[v.ID] = true
			}
		}
		if name == "group.common" {
			field = "abs_groups"
			identity = "group_guid"
		}
		if name == "group.mentions" {
			field = "in_chat_members"
			identity = "member_guid"
		}
		rows, ok := response[field].([]any)
		if !ok || len(rows) > 1000 {
			return nil, protocolError()
		}
		out := []object{}
		seen := map[string]bool{}
		for _, v := range rows {
			row := asObject(v)
			id := row.str(identity)
			if id == "" && name == "group.common" {
				id = row.str("object_guid")
			}
			if (Contract{}).ValidatePeer(peerFromGUID(id)) != nil || seen[id] {
				return nil, protocolError()
			}
			seen[id] = true
			if (name == "contacts.online" || name == "peers.get") && !wanted[id] {
				return nil, protocolError()
			}
			if name == "group.common" && peerFromGUID(id).Type != "group" || name == "group.mentions" && peerFromGUID(id).Type != "user" {
				return nil, protocolError()
			}
			item := publicExtended(row)
			item[identity] = id
			for _, key := range []string{"last_online", "online_time"} {
				switch v := row[key].(type) {
				case string:
					if len(v) > 128 {
						return nil, protocolError()
					}
					item[key] = v
				case json.Number:
					item[key] = v
				}
			}
			out = append(out, item)
		}
		return object{field: out}, nil
	case "group.online":
		n, ok := response["online_count"].(json.Number)
		if !ok {
			return nil, protocolError()
		}
		value, err := n.Int64()
		if err != nil || value < 0 {
			return nil, protocolError()
		}
		return object{"online_count": value}, nil
	case "search.messages":
		rows, ok := response["messages"].([]any)
		if !ok || len(rows) > 1000 {
			return nil, protocolError()
		}
		items := []object{}
		seen := map[string]bool{}
		for _, v := range rows {
			row := asObject(v)
			guid := row.str("object_guid")
			message := asObject(row["message"])
			id := message.str("message_id")
			if (Contract{}).ValidatePeer(peerFromGUID(guid)) != nil || !validMessageID(id) || seen[guid+":"+id] {
				return nil, protocolError()
			}
			seen[guid+":"+id] = true
			if nested := message.str("object_guid"); nested != "" && nested != guid {
				return nil, protocolError()
			}
			// Global search carries the message timestamp on its result row. Do
			// not reinterpret missing creation time as an observation timestamp.
			if message["time"] == nil && row.num("time") > 0 {
				copy := object{}
				for key, value := range message {
					copy[key] = value
				}
				copy["time"] = row["time"]
				message = copy
			}
			event, err := c.projectMessage(guid, object{"action": "New", "object_guid": guid, "message_id": id, "message": message})
			if err != nil {
				return nil, err
			}
			if event.Media != nil && c.cfg.SaveMediaReference != nil {
				saved, err := c.cfg.SaveMediaReference(ctx, event.Peer, event.MessageID, *event.Media)
				if err != nil {
					return nil, err
				}
				event.Message.Media.DownloadSupported = saved
			}
			items = append(items, object{"peer": event.Peer, "message": event.Message, "payload": event.Payload})
		}
		out := object{"items": items, "complete": false}
		if cursor := response.str("next_start_id"); cursor != "" {
			if !domains.ValidOpaqueID(cursor) {
				return nil, protocolError()
			}
			out["next_start_id"] = cursor
		}
		if more, ok := response["has_continue"].(bool); ok {
			out["has_continue"] = more
		}
		return out, nil
	case "link.resolve":
		link := asObject(response["link"])
		if link.str("type") != "url" || !publicRubikaLink(link.str("link_url"), true) {
			return nil, domains.Unsupported("provider returned a non-communication link")
		}
		return object{"type": "url", "link_url": link.str("link_url")}, nil
	default:
		if status := response.str("status"); status != "" && status != "OK" && status != "Done" {
			return nil, rpcError(status)
		}
		return object{"acknowledged": true}, nil
	}
}
