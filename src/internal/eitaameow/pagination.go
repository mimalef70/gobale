package eitaameow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

type pageCursor struct {
	Version    int          `json:"v"`
	Account    string       `json:"account"`
	Connection string       `json:"connection"`
	Kind       string       `json:"kind"`
	Peer       string       `json:"peer,omitempty"`
	ID         int64        `json:"id"`
	Date       int64        `json:"date"`
	OffsetPeer domains.Peer `json:"offset_peer"`
	Boundaries []string     `json:"boundaries,omitempty"`
}

func cursorInvalid() error {
	return domains.E("INVALID_CURSOR", "cursor is invalid for this account, operation or peer", 400)
}
func (c *Client) cursorIdentity() (string, string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.session.UserID, c.cfg.ConnectionID, c.session.Token
}
func cursorMAC(raw []byte, token string) []byte {
	m := hmac.New(sha256.New, []byte(token))
	_, _ = m.Write([]byte("peykbridge:eitaa:cursor:v1:"))
	_, _ = m.Write(raw)
	return m.Sum(nil)
}
func (c *Client) encodeCursor(cur pageCursor) string {
	account, connection, token := c.cursorIdentity()
	cur.Version, cur.Account, cur.Connection = 1, account, connection
	raw, _ := json.Marshal(cur)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(cursorMAC(raw, token))
}
func (c *Client) decodeCursor(raw, kind, peer string) (pageCursor, error) {
	if raw == "" {
		return pageCursor{Kind: kind, Peer: peer}, nil
	}
	parts := strings.Split(raw, ".")
	if len(raw) > 2048 || len(parts) != 2 {
		return pageCursor{}, cursorInvalid()
	}
	body, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil || len(body) > 1400 {
		return pageCursor{}, cursorInvalid()
	}
	signature, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return pageCursor{}, cursorInvalid()
	}
	account, connection, token := c.cursorIdentity()
	if token == "" || !hmac.Equal(signature, cursorMAC(body, token)) {
		return pageCursor{}, cursorInvalid()
	}
	var cur pageCursor
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&cur) != nil || d.Decode(new(any)) != io.EOF || cur.Version != 1 || cur.Account != account || cur.Connection != connection || cur.Kind != kind || cur.Peer != peer || cur.ID < 1 || cur.ID > 2147483647 || cur.Date < 1 || cur.Date > 2147483647 {
		return pageCursor{}, cursorInvalid()
	}
	if strings.HasPrefix(kind, "dialogs:") {
		if len(cur.Boundaries) == 0 || len(cur.Boundaries) > 32 {
			return pageCursor{}, cursorInvalid()
		}
		for _, b := range cur.Boundaries {
			v, e := hex.DecodeString(b)
			if e != nil || len(v) != 8 || hex.EncodeToString(v) != b {
				return pageCursor{}, cursorInvalid()
			}
		}
	}
	if kind != "history" {
		if (Contract{}).ValidatePeer(cur.OffsetPeer) != nil {
			return pageCursor{}, cursorInvalid()
		}
	}
	return cur, nil
}
func (c *Client) messagePeer(m object) (domains.Peer, error) {
	peer, e := publicPeer(asObject(m["peer_id"]))
	if e != nil {
		return peer, e
	}
	return c.normalizedPeer(peer), nil
}
func (c *Client) normalizedPeer(peer domains.Peer) domains.Peer {
	if peer.Type == "channel" {
		c.mu.RLock()
		known := c.session.Peers["group:"+supergroupPrefix+peer.ID]
		c.mu.RUnlock()
		if known.str("_") == "inputPeerChannel" {
			peer.Type = "group"
			peer.ID = supergroupPrefix + peer.ID
		}
	}
	return peer
}

// Resolve only metadata already owned by this account. Do not recursively call
// dialogs to resolve the offset peer of that same dialogs request.
func (c *Client) cursorPeer(p domains.Peer) (object, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p.Type == "user" && p.ID == c.session.UserID {
		return object{"_": "inputPeerSelf"}, nil
	}
	ref := c.session.Peers[p.Key()]
	if ref == nil {
		return nil, domains.E("CURSOR_REFERENCE_UNAVAILABLE", "cursor peer metadata is no longer available in this account", 409)
	}
	if !peerReferenceMatches(p, ref) {
		return nil, cursorInvalid()
	}
	return ref, nil
}
func (c *Client) dialogs(ctx context.Context, p callRequest, kind string) (json.RawMessage, error) {
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	cur, e := c.decodeCursor(p.Cursor, "dialogs:"+kind, "")
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
	params := object{"offset_date": cur.Date, "offset_id": cur.ID, "offset_peer": offset, "limit": limit, "hash": 0}
	if p.Cursor != "" {
		params["exclude_pinned"] = true
	}
	response, e := c.invoke(ctx, "messages.getDialogs", params, false, false)
	if e != nil {
		return nil, e
	}
	if response.str("_") != "messages.dialogs" && response.str("_") != "messages.dialogsSlice" {
		return nil, protocolError()
	}
	if e = c.rememberEntities(ctx, response); e != nil {
		return nil, e
	}
	dialogs := asObjects(response["dialogs"])
	if len(dialogs) > limit+100 {
		return nil, protocolError()
	}
	names := map[string]string{}
	for _, u := range asObjects(response["users"]) {
		names["user:"+strconv.FormatInt(u.num("id"), 10)] = strings.TrimSpace(u.str("first_name") + " " + u.str("last_name"))
	}
	for _, g := range asObjects(response["chats"]) {
		peer, err := entityPeer(g)
		if err != nil {
			return nil, err
		}
		names[peer.Key()] = g.str("title")
	}
	messages := map[string]object{}
	for _, m := range asObjects(response["messages"]) {
		peer, e := c.messagePeer(m)
		if e != nil {
			continue
		}
		messages[peer.Key()+":"+strconv.FormatInt(m.num("id"), 10)] = m
	}
	items := []object{}
	seen := map[string]bool{}
	var next pageCursor
	for _, dialog := range dialogs {
		if dialog.str("_") != "dialog" {
			return nil, protocolError()
		}
		peer, e := publicPeer(asObject(dialog["peer"]))
		if e != nil {
			return nil, e
		}
		peer = c.normalizedPeer(peer)
		if seen[peer.Key()] {
			return nil, protocolError()
		}
		seen[peer.Key()] = true
		top := dialog.num("top_message")
		m := messages[peer.Key()+":"+strconv.FormatInt(top, 10)]
		if kind == "" || peer.Type == kind {
			row := object{"peer": peer, "title": names[peer.Key()], "unread_count": dialog.num("unread_count")}
			if top > 0 {
				row["message_id"] = strconv.FormatInt(top, 10)
			}
			if m.num("date") > 0 {
				row["date"] = m.num("date")
			}
			items = append(items, row)
		}
		if dialog["pinned"] != true && top > 0 && m.num("date") > 0 {
			next = pageCursor{Kind: "dialogs:" + kind, ID: top, Date: m.num("date"), OffsetPeer: peer}
		}
	}
	result := object{"items": items, "complete": false, "has_more": false}
	if response.str("_") == "messages.dialogsSlice" && len(dialogs) > 0 {
		advancing := next.ID > 0 && (p.Cursor == "" || next.Date < cur.Date || (next.Date == cur.Date && (next.ID != cur.ID || next.OffsetPeer.Key() != cur.OffsetPeer.Key())))
		if advancing {
			boundary := sha256.Sum256([]byte(strconv.FormatInt(next.ID, 10) + "|" + next.OffsetPeer.Key()))
			key := hex.EncodeToString(boundary[:8])
			if p.Cursor != "" && next.Date == cur.Date {
				if slices.Contains(cur.Boundaries, key) || len(cur.Boundaries) >= 32 {
					advancing = false
				} else {
					next.Boundaries = append(append([]string{}, cur.Boundaries...), key)
				}
			} else {
				next.Boundaries = []string{key}
			}
		}
		if advancing {
			result["next_cursor"] = c.encodeCursor(next)
			result["has_more"] = true
		} else {
			result["incomplete"] = true
			result["pagination_stop_reason"] = "non_advancing_cursor"
		}
	}
	return json.Marshal(result)
}
func (c *Client) history(ctx context.Context, p callRequest, input object) (json.RawMessage, error) {
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	cur, e := c.decodeCursor(p.Cursor, "history", p.Peer.Key())
	if e != nil {
		return nil, e
	}
	// Controlled native and official-Web reads returned an old window for
	// limit=5 but the correct offset window for limit=50 (2026-10-10). A request
	// for 100 returned only 50 rows with a placeholder count of 1000. Use the
	// single observed 50-row profile even when the public limit is larger;
	// otherwise that provider cap incorrectly terminates pagination. Truncate
	// smaller public pages locally, without trusting count or retrying reads.
	const wireLimit = 50
	response, e := c.invoke(ctx, "messages.getHistory", object{"peer": input, "offset_id": cur.ID, "offset_date": cur.Date, "add_offset": 0, "limit": wireLimit, "max_id": 0, "min_id": 0, "hash": 0}, false, false)
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
	if len(messages) > wireLimit {
		return nil, protocolError()
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].num("id") > messages[j].num("id") })
	items := []*domains.Message{}
	omittedNotices := 0
	seen := map[string]bool{}
	var next pageCursor
	for _, m := range messages {
		peer, err := c.messagePeer(m)
		if err != nil || peer.Key() != p.Peer.Key() {
			return nil, protocolError()
		}
		if m.num("id") == 0 && m.str("_") == "messageService" && asObject(m["action"]).str("_") == "messageActionChannelMigrateFrom" {
			event, err := c.projectMessage(m, "message")
			if err != nil || event.Peer.Key() != p.Peer.Key() {
				return nil, protocolError()
			}
			omittedNotices++
			continue
		}
		if p.Cursor != "" && m.num("id") >= cur.ID {
			continue
		}
		if m.num("id") <= 0 {
			return nil, protocolError()
		}
		if len(items) >= limit {
			continue
		}
		event, e := c.projectReadEvent(ctx, m, p.Peer)
		if e != nil {
			return nil, e
		}
		if event.Peer.Key() != p.Peer.Key() {
			return nil, protocolError()
		}
		if seen[event.MessageID] {
			continue
		}
		seen[event.MessageID] = true
		items = append(items, event.Message)
		if next.ID == 0 || m.num("id") < next.ID {
			next = pageCursor{Kind: "history", Peer: p.Peer.Key(), ID: m.num("id"), Date: m.num("date")}
		}
	}
	result := object{"items": items, "complete": false, "has_more": false}
	if omittedNotices > 0 {
		result["omitted_service_notices"] = omittedNotices
	}
	if len(items) == limit || len(messages) == wireLimit {
		if next.ID > 0 && (p.Cursor == "" || next.ID < cur.ID) {
			result["next_cursor"] = c.encodeCursor(next)
			result["has_more"] = true
		} else {
			result["incomplete"] = true
			result["pagination_stop_reason"] = "non_advancing_cursor"
		}
	}
	return json.Marshal(result)
}

// Read projections only expose downloadable attachments after a private,
// connection-scoped reference has been durably registered by the gateway.
func (c *Client) projectReadEvent(ctx context.Context, m object, expected domains.Peer) (domains.Event, error) {
	event, err := c.projectMessage(m, "message")
	if err != nil {
		return domains.Event{}, err
	}
	if event.Peer.Key() != expected.Key() {
		return domains.Event{}, protocolError()
	}
	if event.Message == nil {
		return domains.Event{}, protocolError()
	}
	if event.Message.Media != nil {
		event.Message.Media.DownloadSupported = false
	}
	if event.Media != nil && c.cfg.SaveMediaReference != nil {
		saved, err := c.cfg.SaveMediaReference(ctx, event.Peer, event.MessageID, *event.Media)
		if err != nil {
			return domains.Event{}, err
		}
		event.Message.Media.DownloadSupported = saved
	}
	return event, nil
}
