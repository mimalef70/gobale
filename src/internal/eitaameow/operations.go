package eitaameow

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

type callRequest struct {
	Peer       domains.Peer `json:"peer"`
	SourcePeer domains.Peer `json:"source_peer"`
	MessageID  string       `json:"message_id"`
	RequestID  string       `json:"request_id"`
	Text       string       `json:"message"`
	Query      string       `json:"query"`
	Limit      int          `json:"limit"`
	OffsetID   string       `json:"offset_id"`
	OffsetDate int64        `json:"offset_date"`
	Cursor     string       `json:"cursor"`
	JustMine   *bool        `json:"just_mine"`
	HideSender bool         `json:"hide_sender"`
}

func (c *Client) rememberEntities(ctx context.Context, response object) error {
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.mu.RLock()
	next := c.session
	next.Peers = make(map[string]object, len(c.session.Peers))
	for k, v := range c.session.Peers {
		next.Peers[k] = v
	}
	c.mu.RUnlock()
	changed := false
	for _, key := range []string{"users", "chats", "items"} {
		for _, entity := range asObjects(response[key]) {
			id := entity.num("id")
			if id <= 0 {
				continue
			}
			kind := entity.str("_")
			typ := ""
			var ref object
			switch kind {
			case "user":
				if _, ok := entity["access_hash"]; !ok {
					continue
				}
				typ = "user"
				ref = object{"_": "inputPeerUser", "user_id": id, "access_hash": entity.num("access_hash")}
			case "chat":
				typ = "group"
				ref = object{"_": "inputPeerChat", "chat_id": id}
			case "channel":
				if _, ok := entity["access_hash"]; !ok {
					continue
				}
				typ = "channel"
				if entity["megagroup"] == true {
					typ = "group"
				}
				ref = object{"_": "inputPeerChannel", "channel_id": id, "access_hash": entity.num("access_hash")}
			default:
				continue
			}
			publicID := strconv.FormatInt(id, 10)
			if typ == "group" && ref.str("_") == "inputPeerChannel" {
				publicID = supergroupPrefix + publicID
			}
			k := typ + ":" + publicID
			if old := next.Peers[k]; old != nil && old.str("_") != ref.str("_") {
				// Classic chats and channel-backed supergroups have separate wire
				// namespaces. The same public key must never change its target.
				c.setStatus("", "", "gap_detected", "PEER_NAMESPACE_CONFLICT")
				return domains.E("PEER_NAMESPACE_CONFLICT", "Eitaa returned conflicting peer namespaces", 502)
			}
			old, _ := json.Marshal(next.Peers[k])
			newRef, _ := json.Marshal(ref)
			if string(old) != string(newRef) {
				next.Peers[k] = ref
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	if len(next.Peers) > 6000 {
		return domains.E("PROVIDER_LIMIT", "Eitaa peer cache limit reached", 503)
	}
	if c.cfg.PersistSession != nil {
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		session := &domains.Session{Provider: domains.ProviderEitaa, Version: sessionVersion, UserID: next.UserID, Data: raw}
		if err = c.cfg.PersistSession(ctx, session); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.session = next
	c.mu.Unlock()
	return nil
}
func (c *Client) inputPeer(ctx context.Context, p domains.Peer) (object, error) {
	if err := (Contract{}).ValidatePeer(p); err != nil {
		return nil, err
	}
	c.mu.RLock()
	self := c.session.UserID
	known := c.session.Peers[p.Key()]
	c.mu.RUnlock()
	if p.Type == "user" && p.ID == self {
		return object{"_": "inputPeerSelf"}, nil
	}
	if known != nil {
		if !peerReferenceMatches(p, known) {
			return nil, domains.E("PEER_NAMESPACE_CONFLICT", "Eitaa peer reference does not match its namespace", 502)
		}
		return known, nil
	}
	if p.Type == "group" { // Classic groups do not use an access hash. A known supergroup
		// is resolved from account-owned metadata before selecting its wire type.
		_, err := c.dialogs(ctx, callRequest{Limit: 100}, "")
		if err != nil {
			return nil, err
		}
		c.mu.RLock()
		known = c.session.Peers[p.Key()]
		c.mu.RUnlock()
		if known != nil {
			if !peerReferenceMatches(p, known) {
				return nil, domains.E("PEER_NAMESPACE_CONFLICT", "Eitaa peer reference does not match its namespace", 502)
			}
			return known, nil
		}
	} else {
		_, err := c.dialogs(ctx, callRequest{Limit: 100}, "")
		if err != nil {
			return nil, err
		}
		c.mu.RLock()
		known = c.session.Peers[p.Key()]
		c.mu.RUnlock()
		if known != nil {
			if !peerReferenceMatches(p, known) {
				return nil, domains.E("PEER_NAMESPACE_CONFLICT", "Eitaa peer reference does not match its namespace", 502)
			}
			return known, nil
		}
	}
	return nil, domains.E("PEER_NOT_FOUND", "peer is not available in this Eitaa account's resolved metadata", 404)
}
func publicUser(o object) object {
	out := object{"id": strconv.FormatInt(o.num("id"), 10)}
	for _, key := range []string{"first_name", "last_name", "username"} {
		if value := o.str(key); value != "" {
			out[key] = value
		}
	}
	return out
}
func (c *Client) Send(ctx context.Context, r domains.SendRequest) (domains.SendResult, error) {
	if err := (Contract{}).ValidateSend(r); err != nil {
		return domains.SendResult{}, err
	}
	rid, err := strconv.ParseInt(r.RequestID, 10, 64)
	if err != nil || rid <= 0 {
		return domains.SendResult{}, domains.E("INVALID_REQUEST_ID", "persist a positive int64 request ID before sending", 400)
	}
	peer, err := c.inputPeer(ctx, r.Peer)
	if err != nil {
		return domains.SendResult{}, err
	}
	if r.Kind != "" && r.Kind != "text" {
		return c.sendMedia(ctx, r, peer, rid)
	}
	params := object{"peer": peer, "message": r.Text, "random_id": rid}
	if r.ReplyMessageID != "" {
		params["reply_to_msg_id"], _ = messageNumber(r.ReplyMessageID)
	}
	response, err := c.invoke(ctx, "messages.sendMessage", params, true, false)
	if err != nil {
		return domains.SendResult{}, err
	}
	return sendResult(response, rid)
}
func sendResult(o object, rid int64) (domains.SendResult, error) {
	if o.str("_") == "updateShortSentMessage" && o.num("id") > 0 && o.num("date") > 0 {
		return domains.SendResult{MessageID: strconv.FormatInt(o.num("id"), 10), Date: time.Unix(o.num("date"), 0).UTC()}, nil
	}
	id := int64(0)
	for _, update := range asObjects(o["updates"]) {
		if update.str("_") == "updateMessageID" && update.num("random_id") == rid && update.num("id") > 0 {
			id = update.num("id")
		}
	}
	if id > 0 {
		result := domains.SendResult{MessageID: strconv.FormatInt(id, 10)}
		for _, update := range asObjects(o["updates"]) {
			m := asObject(update["message"])
			if m.num("id") == id && m.num("date") > 0 {
				result.Date = time.Unix(m.num("date"), 0).UTC()
			}
		}
		return result, nil
	}
	return domains.SendResult{}, &domains.Error{Code: "SEND_UNKNOWN", Message: "Eitaa did not supply a verified send identity", HTTP: 502, Ambiguous: true}
}
func (c *Client) Call(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	// request_id is injected by the gateway only after its outbox commit; it is
	// removed before validating the consumer portion against the public contract.
	var fields map[string]json.RawMessage
	if len(raw) > 128<<10 || domains.ValidateJSONObject(raw) != nil || json.Unmarshal(raw, &fields) != nil {
		return nil, domains.E("INVALID_REQUEST", "invalid operation payload", 400)
	}
	ridRaw := fields["request_id"]
	delete(fields, "request_id")
	publicRaw, _ := json.Marshal(fields)
	normalized, _, err := (Contract{}).NormalizeOperation(name, publicRaw)
	if err != nil {
		return nil, err
	}
	var p callRequest
	if json.Unmarshal(normalized, &p) != nil {
		return nil, protocolError()
	}
	_ = json.Unmarshal(ridRaw, &p.RequestID)
	mutating := false
	for _, op := range (Contract{}).Operations() {
		if op.Operation == name {
			mutating = op.Mode == "mutation"
		}
	}
	rid := int64(0)
	if mutating {
		rid, err = strconv.ParseInt(p.RequestID, 10, 64)
		if err != nil || rid <= 0 {
			return nil, domains.E("INVALID_REQUEST_ID", "persist a positive request ID before mutation", 400)
		}
	}
	if result, handled, callErr := c.contentSendCall(ctx, name, normalized, rid); handled {
		return result, callErr
	}
	if result, handled, callErr := c.moderationCall(ctx, name, normalized, rid); handled {
		return result, callErr
	}
	if result, handled, callErr := c.adminCall(ctx, name, normalized); handled {
		return result, callErr
	}
	if result, handled, callErr := c.assetCall(ctx, name, normalized, rid); handled {
		return result, callErr
	}
	if result, handled, callErr := c.readCall(ctx, name, normalized); handled {
		return result, callErr
	}
	if result, handled, callErr := c.profileCall(ctx, name, normalized); handled {
		return result, callErr
	}
	if result, handled, callErr := c.moreCall(ctx, name, normalized, rid); handled {
		return result, callErr
	}
	if result, handled, callErr := c.extendedCall(ctx, name, normalized, rid); handled {
		return result, callErr
	}
	if name == "chat.list" || name == "group.list" || name == "channel.list" {
		kind := ""
		if name == "group.list" {
			kind = "group"
		}
		if name == "channel.list" {
			kind = "channel"
		}
		return c.dialogs(ctx, p, kind)
	}
	if name == "account.info" {
		o, e := c.invoke(ctx, "users.getUsers", object{"id": []object{{"_": "inputUserSelf"}}}, false, false)
		if e != nil {
			return nil, e
		}
		items := asObjects(o["items"])
		if len(items) != 1 {
			return nil, protocolError()
		}
		return json.Marshal(publicUser(items[0]))
	}
	if name == "contacts.list" || name == "contacts.search" {
		method, params := "contacts.getContacts", object{"hash": 0}
		if name == "contacts.search" {
			limit := p.Limit
			if limit == 0 {
				limit = 50
			}
			method = "contacts.search"
			params = object{"q": p.Query, "limit": limit}
		}
		o, e := c.invoke(ctx, method, params, false, false)
		if e != nil {
			return nil, e
		}
		if e = c.rememberEntities(ctx, o); e != nil {
			return nil, e
		}
		items := []object{}
		for _, u := range asObjects(o["users"]) {
			items = append(items, publicUser(u))
		}
		return json.Marshal(object{"items": items})
	}
	peer, err := c.inputPeer(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	id := int64(0)
	if p.MessageID != "" {
		id, _ = messageNumber(p.MessageID)
	}
	method := ""
	var params object
	switch name {
	case "chat.history", "chat.messages":
		return c.history(ctx, p, peer)
	case "message.edit":
		method = "messages.editMessage"
		params = object{"peer": peer, "id": id, "message": p.Text}
	case "message.read":
		method = "messages.readHistory"
		params = object{"peer": peer, "max_id": id}
		if peer.str("_") == "inputPeerChannel" {
			method = "channels.readHistory"
			params = object{"channel": inputChannel(peer), "max_id": id}
		}
	case "message.delete":
		method = "messages.deleteMessages"
		params = object{"id": []int64{id}, "revoke": !(*p.JustMine)}
		if peer.str("_") == "inputPeerChannel" {
			if *p.JustMine {
				return nil, domains.Unsupported("own-view channel deletion")
			}
			method = "channels.deleteMessages"
			params = object{"channel": inputChannel(peer), "id": []int64{id}}
		}
	case "message.forward":
		source, e := c.inputPeer(ctx, p.SourcePeer)
		if e != nil {
			return nil, e
		}
		method = "messages.forwardMessages"
		params = object{"from_peer": source, "to_peer": peer, "id": []int64{id}, "random_id": []int64{rid}, "drop_author": p.HideSender}
	default:
		return nil, domains.Unsupported(name)
	}
	response, err := c.invoke(ctx, method, params, true, false)
	if err != nil {
		return nil, err
	}
	if name == "message.forward" {
		result, e := sendResult(response, rid)
		if e != nil {
			return nil, e
		}
		return json.Marshal(result)
	}
	return json.Marshal(object{"acknowledged": true})
}
func inputChannel(p object) object {
	return object{"_": "inputChannel", "channel_id": p.num("channel_id"), "access_hash": p.num("access_hash")}
}
