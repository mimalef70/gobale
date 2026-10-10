package eitaameow

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

type channelCheckpoint struct {
	Version     int    `json:"version"`
	Account     string `json:"account"`
	Channel     string `json:"channel"`
	PTS         int64  `json:"pts"`
	Gap         bool   `json:"gap,omitempty"`
	GapReason   string `json:"gap_reason,omitempty"`
	Unsupported bool   `json:"unsupported_updates,omitempty"`
	More        bool   `json:"more,omitempty"`
}

func channelCheckpointJSON(cp channelCheckpoint) string { b, _ := json.Marshal(cp); return string(b) }

// Each iteration visits at most four channels and rotates across the resolved
// account-owned references. No user-supplied hash or global channel cache is used.
func (c *Client) pollChannels(ctx context.Context, sink domains.BatchSink) error {
	c.mu.Lock()
	c.channelMore = false
	c.mu.Unlock()
	c.mu.RLock()
	refs := make(map[string]object)
	for _, ref := range c.session.Peers {
		if ref.str("_") == "inputPeerChannel" {
			refs[strconv.FormatInt(ref.num("channel_id"), 10)] = ref
		}
	}
	previous := c.channelCursor
	c.mu.RUnlock()
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	start := sort.SearchStrings(ids, previous)
	if start < len(ids) && ids[start] == previous {
		start++
	}
	ids = append(append([]string(nil), ids[start:]...), ids[:start]...)
	var first error
	for _, id := range ids[:min(4, len(ids))] {
		if err := c.pollChannelPage(ctx, id, refs[id], sink); err != nil && first == nil {
			first = err
		}
		c.mu.Lock()
		c.channelCursor = id
		c.mu.Unlock()
	}
	return first
}

func (c *Client) pollChannelPage(ctx context.Context, id string, ref object, sink domains.BatchSink) error {
	if c.cfg.PollPermit != nil {
		release, err := c.cfg.PollPermit(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	if c.cfg.LoadCheckpoint == nil || sink == nil {
		return domains.E("BATCH_SINK_REQUIRED", "channel recovery requires durable callbacks", 500)
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	scope := "eitaa.channel." + id
	expected, err := c.cfg.LoadCheckpoint(ctx, scope)
	if err != nil {
		return err
	}
	cp := channelCheckpoint{Version: 2, Account: account, Channel: id}
	events := []domains.Event{}
	marker := func(phase string) {
		payload, _ := json.Marshal(object{"phase": phase, "initial_history_imported": false})
		events = append(events, domains.Event{Provider: domains.ProviderEitaa, ID: eventHash(scope + "|" + account + "|" + phase + "|" + expected), Type: "connection.recovery", Peer: c.normalizedPeer(domains.Peer{Type: "channel", ID: id}), Direction: "unknown", Time: time.Now().UTC(), Payload: payload})
	}
	if expected == "" {
		response, err := c.invoke(ctx, "channels.getFullChannel", object{"channel": inputChannel(ref)}, false, false)
		if err != nil {
			return err
		}
		full := asObject(response["full_chat"])
		if full.str("_") != "channelFull" || strconv.FormatInt(full.num("id"), 10) != id || full.num("pts") < 0 {
			return protocolError()
		}
		cp.PTS = full.num("pts")
		marker("starting_now")
	} else {
		if len(expected) > 4096 || json.Unmarshal([]byte(expected), &cp) != nil || (cp.Version != 1 && cp.Version != 2) || cp.Account != account || cp.Channel != id || cp.PTS < 0 {
			return domains.E("INVALID_CHECKPOINT", "stored channel checkpoint does not match account and channel", 500)
		}
		if cp.Version == 1 {
			cp.Version = 2
			if cp.Gap {
				cp.GapReason = "legacy_unclassified"
			}
		}
		if !validGapReason(cp.Gap, cp.GapReason, true) {
			return domains.E("INVALID_CHECKPOINT", "stored channel gap provenance is invalid", 500)
		}
		response, err := c.invoke(ctx, "updates.getChannelDifference", object{"channel": inputChannel(ref), "filter": object{"_": "channelMessagesFilterEmpty"}, "pts": cp.PTS, "limit": 100}, false, false)
		if err != nil {
			return err
		}
		before := cp.PTS
		cp.More = response["final"] != true
		switch response.str("_") {
		case "updates.channelDifferenceEmpty":
			cp.PTS = response.num("pts")
		case "updates.channelDifference", "updates.channelDifferenceTooLong":
			messages := asObjects(response["new_messages"])
			cp.PTS = response.num("pts")
			if response.str("_") == "updates.channelDifferenceTooLong" {
				cp.Gap = true
				cp.GapReason = "channel_difference_too_long"
				dialog := asObject(response["dialog"])
				peer, e := publicPeer(asObject(dialog["peer"]))
				if e != nil || peer.Type != "channel" || peer.ID != id {
					return protocolError()
				}
				cp.PTS = dialog.num("pts")
				messages = asObjects(response["messages"])
				marker("gap_detected")
			}
			if err = c.rememberEntities(ctx, response); err != nil {
				return err
			}
			for _, m := range messages {
				event, err := c.projectMessage(m, "message")
				if err != nil || peerWireID(event.Peer) != id || (event.Peer.Type != "channel" && !isSupergroup(event.Peer)) {
					return protocolError()
				}
				events = append(events, event)
			}
			for _, u := range asObjects(response["other_updates"]) {
				if u.str("_") == "updateChannelTooLong" || u.str("_") == "updateChannel" {
					event, err := c.projectChannelNotice(u, id, expected)
					if err != nil {
						return err
					}
					events = append(events, event)
					continue
				}
				if u.str("_") == "updateMessageID" {
					continue // no reviewed acceptance proof
				}
				if u.str("_") == "updateEditChannelMessage" || u.str("_") == "updateNewChannelMessage" {
					kind := "message"
					if u.str("_") == "updateEditChannelMessage" {
						kind = "message.edited"
					}
					event, err := c.projectMessage(asObject(u["message"]), kind)
					if err != nil || peerWireID(event.Peer) != id || (event.Peer.Type != "channel" && !isSupergroup(event.Peer)) {
						return protocolError()
					}
					if err = applyMessageUpdateRevision(&event, u); err != nil {
						return err
					}
					events = append(events, event)
					continue
				}
				projected, handled, gap, err := c.projectStateUpdate(u, id)
				if err != nil {
					return err
				}
				if handled {
					if len(events)+len(projected) > 1000 {
						return protocolError()
					}
					events = append(events, projected...)
					if gap {
						cp.Gap, cp.GapReason = true, "unresolved_message_peer"
					}
					continue
				}
				cp.Unsupported = true
				encoded, _ := json.Marshal(u)
				payload, _ := json.Marshal(object{"supported": false, "variant": u.str("_")})
				events = append(events, domains.Event{Provider: domains.ProviderEitaa, ID: eventHash(scope + "|" + expected + "|" + string(encoded)), Type: "protocol.unsupported_update", Peer: c.normalizedPeer(domains.Peer{Type: "channel", ID: id}), Direction: "unknown", Time: time.Now().UTC(), Payload: payload})
			}
		default:
			return protocolError()
		}
		if cp.PTS < before || len(events) > 1000 {
			return protocolError()
		}
		if response["final"] != true && cp.PTS == before {
			return protocolError()
		}
	}
	if err := sink(ctx, domains.EventBatch{Events: events, Checkpoints: []domains.CheckpointTransition{{Scope: scope, Expected: expected, Next: channelCheckpointJSON(cp)}}}); err != nil {
		return err
	}
	c.mu.Lock()
	if cp.Gap && (c.channelGapReason == "" || cp.GapReason != "legacy_unclassified") {
		c.channelGapReason = cp.GapReason
	}
	c.channelMore = c.channelMore || cp.More
	c.status.UnsupportedUpdatesObserved = c.status.UnsupportedUpdatesObserved || cp.Unsupported
	c.mu.Unlock()
	return nil
}

// Only reviewed routing metadata is public. An invalidation is a request to
// poll the channel-owned cursor, not evidence of an expired difference state.
func (c *Client) projectChannelNotice(u object, scope, provenance string) (domains.Event, error) {
	id := u.num("channel_id")
	if id <= 0 {
		return domains.Event{}, protocolError()
	}
	channel := strconv.FormatInt(id, 10)
	if scope != "" && scope != channel {
		return domains.Event{}, protocolError()
	}
	payload := object{"provider_update": u.str("_"), "timestamp_source": "observed", "recovery_requested": u.str("_") == "updateChannelTooLong"}
	if v, present := u["pts"]; present {
		n, err := integer(v)
		if err != nil || n < 0 || n > 2147483647 {
			return domains.Event{}, protocolError()
		}
		payload["pts"] = strconv.FormatInt(n, 10)
	}
	data, _ := json.Marshal(payload)
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	return domains.Event{Provider: domains.ProviderEitaa, ID: eventHash(account + "|channel_notice|" + channel + "|" + provenance + "|" + string(data)), Type: "chat.updated", Peer: c.normalizedPeer(domains.Peer{Type: "channel", ID: channel}), Direction: "unknown", Time: time.Now().UTC(), Payload: data}, nil
}
