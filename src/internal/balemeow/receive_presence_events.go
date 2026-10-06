package balemeow

import (
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"time"
)

func presenceEvents(account string, data []byte) ([]domains.Event, error) {
	u := &wire.PresenceUpdateUnion{}
	if decode(data, u) != nil {
		return nil, protocolError()
	}
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
		v *wire.TypingUpdate
	}{{"presence.typing", u.Typing}, {"presence.typing_stopped", u.TypingStop}} {
		if v := x.v; v != nil {
			p, e := decodePeer(v.Peer)
			if e != nil || v.UserId == 0 || v.TypingType < 0 {
				return nil, protocolError()
			}
			add(x.k, p, "", 0, map[string]any{"user_id": uid(v.UserId), "typing_type": v.TypingType})
		}
	}
	for _, x := range []struct {
		k string
		v *wire.PresenceStateUpdate
	}{{"presence.online", u.Online}, {"presence.offline", u.Offline}} {
		if v := x.v; v != nil {
			if v.UserId == 0 {
				return nil, protocolError()
			}
			b := map[string]any{"device_type": v.DeviceType}
			if v.DeviceCategory != nil {
				b["device_category"] = v.DeviceCategory.Value
			}
			add(x.k, domains.Peer{Type: "user", ID: uid(v.UserId)}, "", 0, b)
		}
	}
	if v := u.LastSeen; v != nil {
		if v.UserId == 0 || v.Date < 0 {
			return nil, protocolError()
		}
		b := map[string]any{"date": sid(v.Date), "device_type": v.DeviceType}
		if v.DeviceCategory != nil {
			b["device_category"] = v.DeviceCategory.Value
		}
		add("presence.last_seen", domains.Peer{Type: "user", ID: uid(v.UserId)}, "", v.Date, b)
	}
	if v := u.Reactions; v != nil {
		p, e := decodePeer(v.Peer)
		if e != nil || v.Rid == 0 {
			return nil, protocolError()
		}
		summaries, e := reactionSummaries(v.Reactions)
		if e != nil {
			return nil, e
		}
		add("message.reactions_changed", p, sid(v.Rid), 0, map[string]any{"reactions": summaries, "reaction_by_me": v.ReactionByMe})
	}
	for _, x := range []struct {
		k string
		v *wire.ReactionPositionUpdate
	}{{"message.new_reaction", u.NewReaction}, {"message.reactions_read_by_me", u.ReactionsReadByMe}} {
		if v := x.v; v != nil {
			p, e := safeExPeer(v.Peer)
			if e != nil || v.Message == nil || v.Message.Rid == 0 || v.Message.Date < 0 {
				return nil, protocolError()
			}
			add(x.k, p, sid(v.Message.Rid), v.Message.Date, map[string]any{"date": sid(v.Message.Date)})
		}
	}
	return out, nil
}
