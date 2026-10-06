package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"strconv"
	"time"
)

type historyMessage struct {
	Peer      domains.Peer    `json:"peer"`
	ID        string          `json:"message_id"`
	SenderID  string          `json:"sender_id"`
	Date      time.Time       `json:"date"`
	Direction string          `json:"direction"`
	State     int32           `json:"provider_state"`
	Payload   json.RawMessage `json:"payload"`
}

func pageArgs(limit int, date string) (int32, int64, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return 0, 0, boundedError("INVALID_REQUEST", "limit must be between 1 and 100", 400)
	}
	if date == "" || date == "0" {
		return int32(limit), 0, nil
	}
	cursor, err := positiveID(date)
	if err != nil {
		return 0, 0, boundedError("INVALID_REQUEST", "date must be a nonnegative decimal millisecond timestamp", 400)
	}
	return int32(limit), cursor, nil
}
func (c *Client) history(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Peer     domains.Peer `json:"peer"`
		Date     string       `json:"date"`
		Limit    int          `json:"limit"`
		LoadMode *int         `json:"load_mode"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid history request", 400)
	}
	mode := int32(2)
	if p.LoadMode != nil {
		if *p.LoadMode < 1 || *p.LoadMode > 3 {
			return nil, boundedError("INVALID_REQUEST", "load_mode must be 1 (forward), 2 (backward) or 3 (both)", 400)
		}
		mode = int32(*p.LoadMode)
	}
	if mode == 3 && p.Date == "" {
		return nil, boundedError("INVALID_REQUEST", "both-direction history requires an explicit date anchor", 400)
	}
	peer, err := c.messagePeer(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	limit, date, err := pageArgs(p.Limit, p.Date)
	if err != nil {
		return nil, err
	}
	// The web client opens backward history at Date.now(); zero is the
	// Unix epoch and correctly returns no earlier messages.
	if p.Date == "" && mode == 2 {
		date = time.Now().UnixMilli()
	}
	data, err := c.readRPC(ctx, "bale.messaging.v2.Messaging", "LoadHistory", &wire.HistoryRequest{Peer: peer, Date: date, LoadMode: mode, Limit: limit})
	if err != nil {
		return nil, err
	}
	reply := &wire.HistoryResponse{}
	if decode(data, reply) != nil || len(reply.History) > int(limit) {
		return nil, protocolError()
	}
	if err := c.rememberConversationRefs(reply.Users, reply.Groups, reply.UserPeers, reply.GroupPeers); err != nil {
		return nil, err
	}
	p.Peer = c.canonicalPeer(p.Peer)
	c.mu.Lock()
	self := ""
	if c.session != nil {
		self = c.session.UserID
	}
	c.mu.Unlock()
	out := struct {
		Messages   []historyMessage `json:"messages"`
		NextDate   string           `json:"next_date,omitempty"`
		BeforeDate string           `json:"before_date,omitempty"`
		AfterDate  string           `json:"after_date,omitempty"`
	}{Messages: []historyMessage{}}
	var oldest, newest int64
	for _, m := range reply.History {
		if validateHistoryItem(m) != nil {
			return nil, protocolError()
		}
		if descriptor := providerMedia(m.Message); descriptor != nil && c.opts.SaveMediaReference != nil {
			if err := c.opts.SaveMediaReference(ctx, p.Peer, strconv.FormatInt(m.Rid, 10), *descriptor); err != nil {
				return nil, err
			}
		}
		sender := strconv.FormatUint(uint64(m.SenderId), 10)
		direction := "incoming"
		if sender == self {
			direction = "outgoing"
		}
		out.Messages = append(out.Messages, historyMessage{Peer: p.Peer, ID: strconv.FormatInt(m.Rid, 10), SenderID: sender, Date: time.UnixMilli(m.Date).UTC(), State: m.State, Direction: direction, Payload: decoratedHistoryPayload(m, c.opts.SaveMediaReference != nil)})
		if len(out.Messages) == 1 || m.Date < oldest {
			oldest = m.Date
		}
		if len(out.Messages) == 1 || m.Date > newest {
			newest = m.Date
		}
	}
	if len(out.Messages) > 0 {
		if mode == 3 {
			// Both is a window around an anchor, not a paginated direction.
			// Return each boundary so callers can continue with mode 1 or 2.
			out.BeforeDate = strconv.FormatInt(oldest, 10)
			out.AfterDate = strconv.FormatInt(newest, 10)
		} else if len(out.Messages) == int(limit) {
			if mode == 1 {
				out.NextDate = strconv.FormatInt(newest, 10)
			} else {
				out.NextDate = strconv.FormatInt(oldest, 10)
			}
		}
	}
	return json.Marshal(out)
}
func (c *Client) dialogs(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Date  string `json:"date"`
		Limit int    `json:"limit"`
	}
	if len(raw) != 0 && json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid chats request", 400)
	}
	limit, date, err := pageArgs(p.Limit, p.Date)
	if err != nil {
		return nil, err
	}
	// The official client maps its initial MAX_SAFE_INTEGER cursor to -1.
	// Explicit date="0" continues to mean the epoch.
	if p.Date == "" {
		date = -1
	}
	data, err := c.readRPC(ctx, "bale.messaging.v2.Messaging", "LoadDialogs", &wire.DialogsRequest{MinDate: date, Limit: limit})
	if err != nil {
		return nil, err
	}
	reply := &wire.DialogsResponse{}
	if decode(data, reply) != nil || len(reply.Dialogs) > int(limit) {
		return nil, protocolError()
	}
	if err := c.rememberConversationRefs(reply.Users, reply.Groups, reply.UserPeers, reply.GroupPeers); err != nil {
		return nil, err
	}
	type dialog struct {
		Peer      domains.Peer    `json:"peer"`
		Unread    int32           `json:"unread_count"`
		Date      time.Time       `json:"date"`
		MessageID string          `json:"message_id,omitempty"`
		Payload   json.RawMessage `json:"payload"`
	}
	out := struct {
		Chats    []dialog `json:"chats"`
		NextDate string   `json:"next_date,omitempty"`
	}{Chats: []dialog{}}
	for _, d := range reply.Dialogs {
		if d.UnreadCount < 0 || d.Date < 0 || validateMessage(d.Message) != nil {
			return nil, protocolError()
		}
		peer, err := decodePeer(d.Peer)
		if err != nil {
			return nil, err
		}
		row := dialog{Peer: c.canonicalPeer(peer), Unread: d.UnreadCount, Date: time.UnixMilli(d.Date).UTC(), Payload: messagePayload(d.Message)}
		if d.Rid != 0 {
			row.MessageID = strconv.FormatInt(d.Rid, 10)
		}
		out.Chats = append(out.Chats, row)
		previous, _ := strconv.ParseInt(out.NextDate, 10, 64)
		if d.SortDate > 0 && (previous == 0 || d.SortDate < previous) {
			out.NextDate = strconv.FormatInt(d.SortDate, 10)
		}
	}
	if len(out.Chats) < int(limit) {
		out.NextDate = ""
	}
	return json.Marshal(out)
}

func historyPayload(m *wire.Message, saved bool) json.RawMessage {
	raw := messagePayload(m)
	if saved && m.GetDocument() != nil {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		p["download_supported"] = true
		raw, _ = json.Marshal(p)
	}
	return raw
}

func decoratedHistoryPayload(m *wire.HistoryItem, saved bool) json.RawMessage {
	raw := decoratedPayload(m.Message, m.QuotedMessage, m.Previous, m.Thread, m.GroupedId, m.AuthorSign)
	var b map[string]any
	_ = json.Unmarshal(raw, &b)
	if m.EditedAt != nil {
		b["edited_at"] = sid(m.EditedAt.Value)
	}
	if m.EditorUserId != nil {
		b["editor_user_id"] = uid(uint32(m.EditorUserId.Value))
	}
	if m.Next != nil {
		b["next_message"] = positionBody(m.Next)
	}
	if m.HasComment != nil {
		b["has_comment"] = m.HasComment.Value
	}
	if saved && providerMedia(m.Message) != nil {
		markDownloadSupported(b)
	}
	out, _ := json.Marshal(b)
	return out
}
func markDownloadSupported(b map[string]any) {
	if b["kind"] == "document" {
		b["download_supported"] = true
	}
	if child, ok := b["content"].(map[string]any); ok {
		markDownloadSupported(child)
	}
}
