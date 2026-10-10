package eitaameow

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

// projectStateUpdate keeps provider state separate from observed time. Several
// layer135 updates carry no date or peer; neither value is guessed from a page.
func (c *Client) projectStateUpdate(u object, channelScope string) ([]domains.Event, bool, bool, error) {
	name := u.str("_")
	switch name {
	case "updateDeleteChannelMessages", "updateDeleteMessages", "updateReadHistoryInbox", "updateReadHistoryOutbox", "updateReadChannelInbox", "updateReadChannelOutbox", "updateReadMessagesContents", "updateChannelReadMessagesContents":
	default:
		return nil, false, false, nil
	}
	payload := object{"provider_update": name, "timestamp_source": "observed"}
	for _, key := range []string{"pts", "pts_count"} {
		if value, present := u[key]; present {
			n, e := integer(value)
			if e != nil || n < 0 || n > 2147483647 {
				return nil, true, false, protocolError()
			}
			payload[key] = strconv.FormatInt(n, 10)
		}
	}
	var peer domains.Peer
	if _, present := u["channel_id"]; present {
		n := u.num("channel_id")
		if n < 1 {
			return nil, true, false, protocolError()
		}
		peer = c.normalizedPeer(domains.Peer{Type: "channel", ID: strconv.FormatInt(n, 10)})
	} else if value := asObject(u["peer"]); value != nil {
		var e error
		peer, e = publicPeer(value)
		if e != nil {
			return nil, true, false, e
		}
		peer = c.normalizedPeer(peer)
	}
	unresolved := peer.ID == ""
	if channelScope != "" && (unresolved || peerWireID(peer) != channelScope || (peer.Type != "channel" && !isSupergroup(peer))) {
		return nil, true, false, protocolError()
	}
	payload["peer_resolved"] = !unresolved
	events := []domains.Event{}
	add := func(kind, mid string, body object) {
		raw, _ := json.Marshal(body)
		identity := kind + "|" + peer.Key() + "|" + mid + "|" + string(raw)
		event := domains.Event{Provider: domains.ProviderEitaa, ID: eventHash(identity), Type: kind, Peer: peer, MessageID: mid, Direction: "unknown", Time: time.Now().UTC(), Payload: raw}
		if name == "updateDeleteChannelMessages" && u.num("pts") > 0 {
			event.MediaRevision = &domains.MediaRevision{Scope: "channel:" + peerWireID(peer), Sequence: u.num("pts")}
		}
		events = append(events, event)
	}
	switch name {
	case "updateReadHistoryInbox", "updateReadHistoryOutbox", "updateReadChannelInbox", "updateReadChannelOutbox":
		if unresolved || u.num("max_id") < 0 || u.num("max_id") > 2147483647 {
			return nil, true, false, protocolError()
		}
		kind, scope := "message.read", "outbox"
		if name == "updateReadHistoryInbox" || name == "updateReadChannelInbox" {
			kind, scope = "message.read_by_me", "inbox"
			if u.num("still_unread_count") < 0 {
				return nil, true, false, protocolError()
			}
			payload["unread_count"] = u.num("still_unread_count")
		}
		payload["max_message_id"] = strconv.FormatInt(u.num("max_id"), 10)
		payload["read_scope"] = scope
		payload["cumulative"] = true
		payload["message_ids_supported"] = false
		add(kind, "", payload)
	default:
		values, ok := u["messages"].([]any)
		if !ok || len(values) > 1000 {
			return nil, true, false, protocolError()
		}
		numbers := []int64{}
		seen := map[int64]bool{}
		for _, v := range values {
			n, e := integer(v)
			if e != nil || n < 1 || n > 2147483647 {
				return nil, true, false, protocolError()
			}
			if !seen[n] {
				numbers = append(numbers, n)
				seen[n] = true
			}
		}
		sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
		ids := []string{}
		for _, n := range numbers {
			ids = append(ids, strconv.FormatInt(n, 10))
		}
		if len(ids) == 0 {
			return events, true, false, nil
		}
		isDelete := name == "updateDeleteMessages" || name == "updateDeleteChannelMessages"
		if unresolved {
			kind := "message.content_read.unresolved"
			if isDelete {
				kind = "message.deleted.unresolved"
			}
			payload["message_ids"] = ids
			payload["reason"] = "provider_omitted_peer"
			add(kind, "", payload)
		} else if isDelete {
			for _, id := range ids {
				add("message.deleted", id, payload)
			}
		} else {
			payload["message_ids"] = ids
			payload["content_state"] = "read"
			add("message.content_read", "", payload)
		}
	}
	return events, true, unresolved, nil
}
