package rubikameow

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/mimalef70/goomni/src/domains"
)

// The reviewed Web client uses inclusive max_id and subtracts one from its
// smallest accepted ID. Derive our boundary from returned items, never from a
// provider cursor beyond a locally truncated page.
func (c *Client) history(ctx context.Context, p callRequest) (json.RawMessage, error) {
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	params := object{"object_guid": p.Peer.ID, "sort": "FromMax", "limit": limit}
	if p.OffsetID != "" {
		params["max_id"] = p.OffsetID
	}
	o, err := c.invoke(ctx, "getMessages", params, false, "")
	if err != nil {
		return nil, err
	}
	rows, ok := o["messages"].([]any)
	if !ok || len(rows) > 1000 {
		return nil, protocolError()
	}
	more, ok := o["has_continue"].(bool)
	if !ok {
		return nil, protocolError()
	}
	upper := int64(0)
	if p.OffsetID != "" {
		upper, _ = strconv.ParseInt(p.OffsetID, 10, 64)
	}
	messages := []object{}
	seen := map[string]bool{}
	for _, value := range rows {
		m := asObject(value)
		if guid := m.str("object_guid"); guid != "" && guid != p.Peer.ID {
			return nil, protocolError()
		}
		id := m.str("message_id")
		if !validMessageID(id) {
			return nil, protocolError()
		}
		n, _ := strconv.ParseInt(id, 10, 64)
		if upper > 0 && n > upper {
			return nil, protocolError()
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		messages = append(messages, m)
	}
	sort.Slice(messages, func(i, j int) bool {
		a, _ := strconv.ParseInt(messages[i].str("message_id"), 10, 64)
		b, _ := strconv.ParseInt(messages[j].str("message_id"), 10, 64)
		return a > b
	})
	more = more || len(messages) > limit
	if len(messages) > limit {
		messages = messages[:limit]
	}
	items := []*domains.Message{}
	for _, m := range messages {
		event, e := c.projectMessage(p.Peer.ID, object{"action": "New", "object_guid": p.Peer.ID, "message_id": m.str("message_id"), "message": m})
		if e != nil {
			return nil, e
		}
		if event.Media != nil && c.cfg.SaveMediaReference != nil {
			saved, e := c.cfg.SaveMediaReference(ctx, event.Peer, event.MessageID, *event.Media)
			if e != nil {
				return nil, e
			}
			event.Message.Media.DownloadSupported = saved
		}
		items = append(items, event.Message)
	}
	result := object{"items": items, "has_continue": more, "complete": false}
	if more {
		if len(items) == 0 {
			return nil, protocolError()
		}
		last, _ := strconv.ParseInt(items[len(items)-1].ID, 10, 64)
		if last <= 1 {
			result["has_continue"] = false
		} else {
			result["next_offset_id"] = strconv.FormatInt(last-1, 10)
		}
	}
	return json.Marshal(result)
}
