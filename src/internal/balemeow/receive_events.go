package balemeow

import (
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"time"
)

func additionalEvents(account string, data []byte, u *wire.UpdateContainer) ([]domains.Event, error) {
	out := []domains.Event{}
	add := func(kind string, p domains.Peer, mid string, at int64, b map[string]any) {
		raw, _ := json.Marshal(b)
		when := time.Now().UTC()
		if at > 0 {
			when = time.UnixMilli(at).UTC()
		}
		out = append(out, domains.Event{ID: eventHash(account + "|" + kind + "|" + p.Key() + "|" + string(data)), Type: kind, AccountID: account, Peer: p, MessageID: mid, Direction: "unknown", Time: when, Payload: raw})
	}
	for _, x := range []struct {
		k string
		v *wire.ChatPeerUpdate
	}{{"chat.cleared", u.ChatCleared}, {"chat.deleted", u.ChatDeleted}} {
		if x.v != nil {
			p, e := decodePeer(x.v.Peer)
			if e != nil {
				return nil, e
			}
			add(x.k, p, "", 0, map[string]any{})
		}
	}
	for _, x := range []struct {
		k string
		v *wire.MessageReceipt
	}{{"message.read", u.Read}, {"message.received", u.Received}} {
		if v := x.v; v != nil {
			p, e := decodePeer(v.Peer)
			if e != nil || v.StartDate < 0 || v.Date < 0 {
				return nil, protocolError()
			}
			add(x.k, p, "", v.Date, map[string]any{"start_date": sid(v.StartDate), "date": sid(v.Date)})
		}
	}
	if v := u.ReadByMe; v != nil {
		p, e := decodePeer(v.Peer)
		if e != nil || v.StartDate < 0 || v.GetUnreadCount().GetValue() < 0 || v.GetEndDate().GetValue() < 0 {
			return nil, protocolError()
		}
		b := map[string]any{"start_date": sid(v.StartDate)}
		if v.UnreadCount != nil {
			b["unread_count"] = v.UnreadCount.Value
		}
		if v.EndDate != nil {
			b["end_date"] = sid(v.EndDate.Value)
		}
		add("message.read_by_me", p, "", v.StartDate, b)
	}
	for _, x := range []struct {
		k, key string
		v      *wire.UserTextUpdate
	}{{"user.username_changed", "username", u.UsernameChanged}, {"user.about_changed", "about", u.AboutChanged}} {
		if v := x.v; v != nil {
			if v.Uid == 0 {
				return nil, protocolError()
			}
			add(x.k, domains.Peer{Type: "user", ID: uid(v.Uid)}, "", 0, map[string]any{x.key: v.GetValue().GetValue()})
		}
	}
	for _, x := range []struct {
		k string
		v *wire.UserIDUpdate
	}{{"user.blocked", u.Blocked}, {"user.unblocked", u.Unblocked}} {
		if v := x.v; v != nil {
			if v.Uid == 0 {
				return nil, protocolError()
			}
			add(x.k, domains.Peer{Type: "user", ID: uid(v.Uid)}, "", 0, map[string]any{})
		}
	}
	if v := u.Pinned; v != nil {
		if v.GroupId == 0 || validateHistoryItem(v.Message) != nil {
			return nil, protocolError()
		}
		m := v.Message
		add("message.pinned", domains.Peer{Type: "group", ID: uid(v.GroupId)}, sid(m.Rid), m.Date, map[string]any{"message_id": sid(m.Rid), "sender_id": uid(m.SenderId), "date": sid(m.Date), "content": json.RawMessage(decoratedPayload(m.Message, m.QuotedMessage, m.Previous, m.Thread, m.GroupedId, m.AuthorSign))})
	}
	if v := u.Unpinned; v != nil {
		if v.GroupId == 0 {
			return nil, protocolError()
		}
		b := map[string]any{}
		id := ""
		if v.Message != nil {
			if v.Message.Rid == 0 || v.Message.Date < 0 {
				return nil, protocolError()
			}
			id = sid(v.Message.Rid)
			b["message_id"] = id
			b["date"] = sid(v.Message.Date)
		}
		add("message.unpinned", domains.Peer{Type: "group", ID: uid(v.GroupId)}, id, 0, b)
	}
	if v := u.Messages; v != nil {
		p, e := safeExPeer(v.Peer)
		if e != nil || len(v.Messages) > 4096 {
			return nil, protocolError()
		}
		for _, m := range v.Messages {
			if validateHistoryItem(m) != nil {
				return nil, protocolError()
			}
			sender := uid(m.SenderId)
			direction := "incoming"
			if sender == account {
				direction = "outgoing"
			}
			id := sid(m.Rid)
			out = append(out, domains.Event{ID: eventHash(account + "|message|" + messageIdentityPeer(p) + "|" + id), Type: "message", AccountID: account, Peer: p, MessageID: id, SenderID: sender, Direction: direction, Time: time.UnixMilli(m.Date).UTC(), Payload: decoratedPayload(m.Message, m.QuotedMessage, m.Previous, m.Thread, m.GroupedId, m.AuthorSign), Media: providerMedia(m.Message)})
		}
	}
	if len(out) > 4096 {
		return nil, protocolError()
	}
	return out, nil
}
