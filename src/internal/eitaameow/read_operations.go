package eitaameow

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func readOperations() []domains.OperationContract {
	add := func(name string, props map[string]domains.FieldSchema, required ...string) domains.OperationContract {
		return domains.OperationContract{Operation: name, Mode: "read", Path: "/operations/" + name, Method: "POST", Description: "Eitaa " + name, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: props, Required: required}}
	}
	return []domains.OperationContract{
		add("messages.search", map[string]domains.FieldSchema{"query": {Type: "string", MinLength: 1, MaxLength: 512}, "cursor": textField(2048), "limit": boundedInt(1, 100)}, "query"),
		add("users.common_chats", map[string]domains.FieldSchema{"user": idField(), "cursor": textField(4096), "limit": boundedInt(1, 100)}, "user"),
		add("poll.voters", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField(), "option": textField(344), "cursor": textField(4096), "limit": boundedInt(1, 100)}, "peer", "message_id"),
		add("message.read_participants", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField()}, "peer", "message_id"),
		add("message.views", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField()}, "peer", "message_id"),
	}
}

type readRequest struct {
	Peer      domains.Peer `json:"peer"`
	MessageID string       `json:"message_id"`
	User      string       `json:"user"`
	Query     string       `json:"query"`
	Option    string       `json:"option"`
	Cursor    string       `json:"cursor"`
	Limit     int          `json:"limit"`
}
type valueCursor struct {
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	Account    string `json:"account"`
	Connection string `json:"connection"`
}

func (c *Client) encodeValueCursor(kind, value string) string {
	account, connection, token := c.cursorIdentity()
	raw, _ := json.Marshal(valueCursor{kind, value, account, connection})
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(cursorMAC(raw, token))
}
func (c *Client) decodeValueCursor(raw, kind string) (string, error) {
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, ".")
	if len(raw) > 4096 || len(parts) != 2 {
		return "", cursorInvalid()
	}
	body, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return "", cursorInvalid()
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return "", cursorInvalid()
	}
	account, connection, token := c.cursorIdentity()
	if token == "" || !hmac.Equal(sig, cursorMAC(body, token)) {
		return "", cursorInvalid()
	}
	var cur valueCursor
	if json.Unmarshal(body, &cur) != nil || cur.Kind != kind || cur.Account != account || cur.Connection != connection || cur.Value == "" || len(cur.Value) > 2048 {
		return "", cursorInvalid()
	}
	return cur.Value, nil
}
func (c *Client) readCall(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, bool, error) {
	known := false
	for _, op := range readOperations() {
		if op.Operation == name {
			known = true
		}
	}
	if !known {
		return nil, false, nil
	}
	var p readRequest
	_ = json.Unmarshal(raw, &p)
	if p.Limit == 0 {
		p.Limit = 50
	}
	if name == "messages.search" {
		out, e := c.globalSearch(ctx, p)
		return out, true, e
	}
	method := ""
	params := object{}
	kind := name + ":" + p.User + ":" + p.Peer.Key() + ":" + p.MessageID + ":" + p.Option
	offset, e := c.decodeValueCursor(p.Cursor, kind)
	if e != nil {
		return nil, true, e
	}
	if name == "users.common_chats" {
		user, e := c.inputUser(ctx, p.User)
		if e != nil {
			return nil, true, e
		}
		maxID := int64(0)
		if offset != "" {
			maxID, e = strconv.ParseInt(offset, 10, 64)
			if e != nil || maxID <= 0 {
				return nil, true, cursorInvalid()
			}
		}
		method = "messages.getCommonChats"
		params = object{"user_id": user, "max_id": maxID, "limit": p.Limit}
	} else {
		peer, e := c.inputPeer(ctx, p.Peer)
		if e != nil {
			return nil, true, e
		}
		id, _ := messageNumber(p.MessageID)
		params["peer"] = peer
		switch name {
		case "poll.voters":
			method = "messages.getPollVotes"
			params["id"] = id
			params["limit"] = p.Limit
			if p.Option != "" {
				option, e := base64.StdEncoding.Strict().DecodeString(p.Option)
				if e != nil || len(option) < 1 || len(option) > 256 {
					return nil, true, domains.E("INVALID_REQUEST", "option must be base64 provider option bytes", 400)
				}
				params["option"] = option
			}
			if offset != "" {
				params["offset"] = offset
			}
		case "message.read_participants":
			method = "messages.getMessageReadParticipants"
			params["msg_id"] = id
		case "message.views":
			method = "messages.getMessagesViews"
			params["id"] = []int64{id}
			params["increment"] = false
		}
	}
	response, e := c.invoke(ctx, method, params, false, false)
	if e != nil {
		return nil, true, e
	}
	if e = c.rememberEntities(ctx, response); e != nil {
		return nil, true, e
	}
	result := object{}
	switch name {
	case "users.common_chats":
		if response.str("_") != "messages.chats" && response.str("_") != "messages.chatsSlice" {
			return nil, true, protocolError()
		}
		rows := asObjects(response["chats"])
		if len(rows) > p.Limit {
			return nil, true, protocolError()
		}
		items := []object{}
		last := int64(0)
		seen := map[string]bool{}
		for _, chat := range rows {
			id := chat.num("id")
			if id < 1 {
				return nil, true, protocolError()
			}
			peer, err := entityPeer(chat)
			if err != nil {
				return nil, true, err
			}
			if seen[peer.Key()] {
				return nil, true, protocolError()
			}
			seen[peer.Key()] = true
			items = append(items, object{"peer": peer, "title": chat.str("title")})
			last = id
		}
		result = object{"items": items, "has_more": false, "complete": false}
		if len(rows) == p.Limit && last > 0 {
			next := strconv.FormatInt(last, 10)
			if next == offset {
				result["incomplete"] = true
				result["pagination_stop_reason"] = "non_advancing_cursor"
			} else {
				result["has_more"] = true
				result["next_cursor"] = c.encodeValueCursor(kind, next)
			}
		}
	case "poll.voters":
		if response.str("_") != "messages.votesList" || response.num("count") < 0 {
			return nil, true, protocolError()
		}
		votes := asObjects(response["votes"])
		if len(votes) > p.Limit {
			return nil, true, protocolError()
		}
		items := []object{}
		for _, v := range votes {
			if v.num("user_id") < 1 || v.num("date") < 0 {
				return nil, true, protocolError()
			}
			options := [][]byte{}
			switch v.str("_") {
			case "messageUserVote":
				b, ok := v["option"].([]byte)
				if !ok {
					return nil, true, protocolError()
				}
				options = append(options, b)
			case "messageUserVoteInputOption":
				if p.Option != "" {
					b, _ := base64.StdEncoding.DecodeString(p.Option)
					options = append(options, b)
				}
			case "messageUserVoteMultiple":
				list, ok := v["options"].([]any)
				if !ok || len(list) > 10 {
					return nil, true, protocolError()
				}
				for _, entry := range list {
					b, ok := entry.([]byte)
					if !ok {
						return nil, true, protocolError()
					}
					options = append(options, b)
				}
			default:
				return nil, true, protocolError()
			}
			encoded := []string{}
			for _, b := range options {
				if len(b) < 1 || len(b) > 256 {
					return nil, true, protocolError()
				}
				encoded = append(encoded, base64.StdEncoding.EncodeToString(b))
			}
			items = append(items, object{"user_id": strconv.FormatInt(v.num("user_id"), 10), "options": encoded, "date": time.Unix(v.num("date"), 0).UTC().Format(time.RFC3339)})
		}
		result = object{"items": items, "count": response.num("count"), "has_more": false}
		next := response.str("next_offset")
		if len(next) > 2048 {
			return nil, true, protocolError()
		}
		if next != "" {
			if next == offset {
				return nil, true, domains.E("NON_ADVANCING_CURSOR", "Eitaa poll cursor did not advance", 502)
			}
			result["has_more"] = true
			result["next_cursor"] = c.encodeValueCursor(kind, next)
		}
	case "message.read_participants":
		rows, ok := response["items"].([]any)
		if !ok || len(rows) > 10000 {
			return nil, true, protocolError()
		}
		ids := []string{}
		seen := map[int64]bool{}
		for _, row := range rows {
			id := object{"id": row}.num("id")
			if id < 1 {
				return nil, true, protocolError()
			}
			if !seen[id] {
				ids = append(ids, strconv.FormatInt(id, 10))
				seen[id] = true
			}
		}
		result = object{"user_ids": ids}
	case "message.views":
		rows := asObjects(response["views"])
		if response.str("_") != "messages.messageViews" || len(rows) != 1 || rows[0].str("_") != "messageViews" {
			return nil, true, protocolError()
		}
		result = object{"message_id": p.MessageID}
		for _, key := range []string{"views", "forwards"} {
			if _, ok := rows[0][key]; ok {
				if rows[0].num(key) < 0 {
					return nil, true, protocolError()
				}
				result[key] = rows[0].num(key)
			}
		}
	}
	out, e := json.Marshal(result)
	return out, true, e
}
func (c *Client) globalSearch(ctx context.Context, p readRequest) (json.RawMessage, error) {
	kind := "global_search:" + eventHash(p.Query)
	cur, e := c.decodeCursor(p.Cursor, kind, "")
	if e != nil {
		return nil, e
	}
	offset := object{"_": "inputPeerEmpty"}
	if p.Cursor != "" {
		offset, e = c.cursorPeer(cur.OffsetPeer)
		if e != nil {
			return nil, e
		}
	}
	response, e := c.invoke(ctx, "messages.searchGlobalExt", object{"flags": 0, "q": p.Query, "offset_date": cur.Date, "offset_peer": offset, "offset_id": cur.ID, "limit": p.Limit}, false, false)
	if e != nil {
		return nil, e
	}
	switch response.str("_") {
	case "messages.messages", "messages.messagesSlice", "messages.channelMessages":
	default:
		return nil, protocolError()
	}
	if e = c.rememberEntities(ctx, response); e != nil {
		return nil, e
	}
	messages := asObjects(response["messages"])
	if len(messages) > p.Limit {
		return nil, protocolError()
	}
	items := []object{}
	seen := map[string]bool{}
	var next pageCursor
	for _, m := range messages {
		peer, e := c.messagePeer(m)
		if e != nil {
			return nil, e
		}
		event, e := c.projectReadEvent(ctx, m, peer)
		if e != nil {
			return nil, e
		}
		key := peer.Key() + ":" + event.MessageID
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, object{"peer": peer, "message": event.Message, "payload": event.Payload})
		next = pageCursor{Kind: kind, ID: m.num("id"), Date: m.num("date"), OffsetPeer: peer}
	}
	result := object{"items": items, "has_more": false, "complete": false}
	if len(messages) == p.Limit && next.ID > 0 {
		if p.Cursor != "" && (next.Date > cur.Date || (next.Date == cur.Date && next.ID == cur.ID && next.OffsetPeer.Key() == cur.OffsetPeer.Key())) {
			result["incomplete"] = true
			result["pagination_stop_reason"] = "non_advancing_cursor"
		} else {
			result["has_more"] = true
			result["next_cursor"] = c.encodeCursor(next)
		}
	}
	return json.Marshal(result)
}
