package eitaameow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

const accountScope = "eitaa.account"

type checkpoint struct {
	Version     int    `json:"version"`
	Account     string `json:"account"`
	PTS         int64  `json:"pts"`
	QTS         int64  `json:"qts"`
	Date        int64  `json:"date"`
	Seq         int64  `json:"seq"`
	Gap         bool   `json:"gap,omitempty"`
	GapReason   string `json:"gap_reason,omitempty"`
	Unsupported bool   `json:"unsupported_updates,omitempty"`
	More        bool   `json:"more,omitempty"`
}

func eventHash(s string) string           { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func checkpointJSON(cp checkpoint) string { data, _ := json.Marshal(cp); return string(data) }
func stateCheckpoint(o object, account string, gap bool) (checkpoint, error) {
	cp := checkpoint{Version: 2, Account: account, PTS: o.num("pts"), QTS: o.num("qts"), Date: o.num("date"), Seq: o.num("seq"), Gap: gap}
	if o.str("_") != "updates.state" || cp.PTS < 0 || cp.QTS < 0 || cp.Date <= 0 || cp.Seq < 0 {
		return checkpoint{}, protocolError()
	}
	return cp, nil
}
func parseCheckpoint(raw, account string) (checkpoint, error) {
	var cp checkpoint
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &cp) != nil || (cp.Version != 1 && cp.Version != 2) || cp.Account != account || cp.PTS < 0 || cp.QTS < 0 || cp.Date <= 0 || cp.Seq < 0 {
		return cp, domains.E("INVALID_CHECKPOINT", "stored Eitaa checkpoint is invalid for this account", 500)
	}
	if cp.Version == 1 {
		cp.Version = 2
		if cp.Gap {
			cp.GapReason = "legacy_unclassified"
		}
	}
	if !validGapReason(cp.Gap, cp.GapReason, false) {
		return cp, domains.E("INVALID_CHECKPOINT", "stored Eitaa gap provenance is invalid", 500)
	}
	return cp, nil
}

func validGapReason(gap bool, reason string, channel bool) bool {
	if !gap {
		return reason == ""
	}
	if reason == "legacy_unclassified" || reason == "unresolved_message_peer" {
		return true
	}
	return (!channel && reason == "difference_too_long") || (channel && reason == "channel_difference_too_long")
}

func (c *Client) setRecoveryStatus(cp checkpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Recovery, c.status.RecoveryIssue = "degraded", cp.GapReason
	if cp.More || c.channelMore {
		c.status.Recovery = "recovering"
	}
	if c.channelGapReason != "" && (cp.GapReason == "" || cp.GapReason == "legacy_unclassified" || c.channelGapReason != "legacy_unclassified") {
		c.status.RecoveryIssue = c.channelGapReason
	}
	if (cp.Gap && cp.GapReason != "legacy_unclassified") || (c.channelGapReason != "" && c.channelGapReason != "legacy_unclassified") {
		c.status.Recovery = "gap_detected"
	}
	c.status.UnsupportedUpdatesObserved = c.status.UnsupportedUpdatesObserved || cp.Unsupported
	c.status.LastError = ""
}

// Connect requires the transactional extension: a sequence of single-event
// callbacks cannot represent an atomic Eitaa difference page safely.
func (c *Client) Connect(context.Context, *domains.Session, domains.Sink) error {
	return domains.E("BATCH_SINK_REQUIRED", "Eitaa requires a durable batch sink", 500)
}
func (c *Client) ConnectBatch(ctx context.Context, s *domains.Session, sink domains.BatchSink) (retErr error) {
	if sink == nil || c.cfg.LoadCheckpoint == nil {
		return domains.E("BATCH_SINK_REQUIRED", "Eitaa requires durable update callbacks", 500)
	}
	if err := (Contract{}).ValidateSession(s); err != nil {
		return err
	}
	if err := c.Disconnect(ctx); err != nil {
		return err
	}
	ps, err := decodeSession(s)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.session = ps
	c.channelCursor, c.channelGapReason, c.channelMore = "", "", false
	c.mu.Unlock()
	c.setStatus("authenticated", "connecting", "recovering", "")
	started := false
	defer func() {
		if !started {
			auth, code := "", "RECOVERY_FAILED"
			var de *domains.Error
			if errors.As(retErr, &de) && de.Code == "AUTH_REQUIRED" {
				auth, code = "auth_required", "AUTH_REQUIRED"
			}
			c.setStatus(auth, "disconnected", "degraded", code)
		}
	}()
	raw, err := c.cfg.LoadCheckpoint(ctx, accountScope)
	if err != nil {
		return err
	}
	var cp checkpoint
	if raw == "" {
		state, e := c.invoke(ctx, "updates.getState", object{}, false, false)
		if e != nil {
			return e
		}
		cp, e = stateCheckpoint(state, ps.UserID, false)
		if e != nil {
			return e
		}
		next := checkpointJSON(cp)
		payload := json.RawMessage(`{"phase":"starting_now","initial_history_imported":false}`)
		event := domains.Event{Provider: domains.ProviderEitaa, ID: eventHash("baseline|" + ps.UserID + "|" + next), Type: "connection.recovery", Direction: "unknown", Time: time.Unix(cp.Date, 0).UTC(), Payload: payload}
		if e = sink(ctx, domains.EventBatch{Events: []domains.Event{event}, Checkpoints: []domains.CheckpointTransition{{Scope: accountScope, Expected: "", Next: next}}}); e != nil {
			return e
		}
		raw = next
	} else {
		cp, err = parseCheckpoint(raw, ps.UserID)
		if err != nil {
			return err
		}
	}
	c.setRecoveryStatus(cp)
	// The connect deadline bounds setup, not the accepted connection lifetime.
	// Disconnect owns cancellation and joins this loop before replacement.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	c.mu.Lock()
	c.cancel = cancel
	c.done = done
	c.mu.Unlock()
	c.setStatus("authenticated", "connected", "degraded", "")
	started = true
	go func() {
		defer close(done)
		timer := time.NewTimer(0)
		defer timer.Stop()
		failures := 0
		for {
			select {
			case <-runCtx.Done():
				return
			case <-timer.C:
			}
			next, e := c.pollPage(runCtx, raw, cp, sink)
			if e == nil {
				raw, cp = checkpointJSON(next), next
				c.setRecoveryStatus(cp)
				e = c.pollChannels(runCtx, sink)
				c.setRecoveryStatus(cp)
			}
			if e != nil {
				if runCtx.Err() != nil {
					return
				}
				failures++
				var de *domains.Error
				if errors.As(e, &de) && de.Code == "AUTH_REQUIRED" {
					c.setStatus("auth_required", "disconnected", "degraded", "AUTH_REQUIRED")
					return
				}
				c.setStatus("", "connected", "degraded", "RECOVERY_FAILED")
				delay := c.cfg.PollInterval * time.Duration(1<<min(failures, 5))
				timer.Reset(pollDelay(delay))
				continue
			}
			failures = 0
			delay := c.cfg.PollInterval
			c.mu.RLock()
			if cp.More || c.channelMore {
				delay = min(delay, 500*time.Millisecond)
			}
			c.mu.RUnlock()
			timer.Reset(pollDelay(delay))
		}
	}()
	return nil
}

func pollDelay(base time.Duration) time.Duration {
	base = min(max(base, time.Millisecond), 50*time.Second)
	return max(time.Millisecond, time.Duration(float64(base)*(0.8+rand.Float64()*0.4)))
}
func (c *Client) pollPage(ctx context.Context, expected string, cp checkpoint, sink domains.BatchSink) (checkpoint, error) {
	if c.cfg.PollPermit != nil {
		release, e := c.cfg.PollPermit(ctx)
		if e != nil {
			return cp, e
		}
		defer release()
	}
	o, err := c.invoke(ctx, "updates.getDifference", object{"pts": cp.PTS, "qts": cp.QTS, "date": cp.Date, "pts_total_limit": 1000}, false, false)
	if err != nil {
		return cp, err
	}
	next := cp
	next.Version = 2
	if next.Gap && next.GapReason == "" {
		next.GapReason = "legacy_unclassified"
	}
	next.More = o.str("_") == "updates.differenceSlice"
	events := []domains.Event{}
	switch o.str("_") {
	case "updates.differenceEmpty":
		next.Date = o.num("date")
		next.Seq = o.num("seq")
	case "updates.difference", "updates.differenceSlice":
		state := asObject(o["state"])
		if o.str("_") == "updates.differenceSlice" {
			state = asObject(o["intermediate_state"])
		}
		next, err = stateCheckpoint(state, cp.Account, cp.Gap)
		if err != nil {
			return cp, err
		}
		next.GapReason, next.Unsupported, next.More = cp.GapReason, cp.Unsupported, o.str("_") == "updates.differenceSlice"
		if next.Gap && next.GapReason == "" {
			next.GapReason = "legacy_unclassified"
		}
		if err = c.rememberEntities(ctx, o); err != nil {
			return cp, err
		}
		for _, m := range asObjects(o["new_messages"]) {
			event, e := c.projectMessage(m, "message")
			if e != nil {
				return cp, e
			}
			events = append(events, event)
		}
		for _, update := range asObjects(o["other_updates"]) {
			name := update.str("_")
			switch name {
			case "updateChannelTooLong", "updateChannel":
				// A channel invalidation requests its independent difference stream;
				// it does not prove that account messages have been lost.
				event, e := c.projectChannelNotice(update, "", expected)
				if e != nil {
					return cp, e
				}
				events = append(events, event)
			case "updateNewMessage", "updateNewChannelMessage", "updateEditMessage", "updateEditChannelMessage":
				kind := "message"
				if name == "updateEditMessage" || name == "updateEditChannelMessage" {
					kind = "message.edited"
				}
				event, e := c.projectMessage(asObject(update["message"]), kind)
				if e != nil {
					return cp, e
				}
				if e = applyMessageUpdateRevision(&event, update); e != nil {
					return cp, e
				}
				events = append(events, event)
			case "updateMessageID": // No proof is emitted until provider acceptance semantics are live-reviewed.
			default:
				projected, handled, gap, e := c.projectStateUpdate(update, "")
				if e != nil {
					return cp, e
				}
				if handled {
					if len(events)+len(projected) > 1000 {
						return cp, protocolError()
					}
					events = append(events, projected...)
					if gap {
						next.Gap, next.GapReason = true, "unresolved_message_peer"
					}
					continue
				}
				next.Unsupported = true
				data, _ := json.Marshal(object{"supported": false, "variant": name})
				encoded, _ := json.Marshal(update)
				events = append(events, domains.Event{Provider: domains.ProviderEitaa, ID: eventHash(cp.Account + "|" + expected + "|" + string(encoded)), Type: "protocol.unsupported_update", Direction: "unknown", Time: time.Unix(next.Date, 0).UTC(), Payload: data})
			}
		}
		if len(asObjects(o["new_encrypted_messages"])) > 0 {
			next.Unsupported = true
			data, _ := json.Marshal(object{"supported": false, "variant": "encrypted_messages", "count": len(asObjects(o["new_encrypted_messages"]))})
			events = append(events, domains.Event{Provider: domains.ProviderEitaa, ID: eventHash(cp.Account + "|encrypted_messages|" + expected), Type: "protocol.unsupported_update", Direction: "unknown", Time: time.Unix(next.Date, 0).UTC(), Payload: data})
		}
	case "updates.differenceTooLong":
		next.Gap = true
		next.GapReason = "difference_too_long"
		next.PTS = o.num("pts")
		payload := json.RawMessage(`{"phase":"gap_detected","reason":"difference_too_long"}`)
		events = append(events, domains.Event{Provider: domains.ProviderEitaa, ID: eventHash(cp.Account + "|gap|" + expected), Type: "connection.recovery", Direction: "unknown", Time: time.Unix(cp.Date, 0).UTC(), Payload: payload})
	default:
		return cp, protocolError()
	}
	if next.PTS < cp.PTS || next.QTS < cp.QTS || next.Date < cp.Date || next.Seq < cp.Seq || len(events) > 1000 {
		return cp, protocolError()
	}
	nextJSON := checkpointJSON(next)
	if nextJSON == expected && len(events) == 0 {
		return cp, nil
	}
	if o.str("_") == "updates.differenceSlice" && next.PTS == cp.PTS && next.QTS == cp.QTS && next.Date == cp.Date && next.Seq == cp.Seq {
		return cp, protocolError()
	}
	if err = sink(ctx, domains.EventBatch{Events: events, Checkpoints: []domains.CheckpointTransition{{Scope: accountScope, Expected: expected, Next: nextJSON}}}); err != nil {
		return cp, err
	}
	return next, nil
}

func (c *Client) projectMessage(m object, kind string) (domains.Event, error) {
	if m.str("_") == "messageService" && m.num("id") == 0 && asObject(m["action"]).str("_") == "messageActionChannelMigrateFrom" {
		return c.projectMigrationNotice(m)
	}
	if m == nil || m.num("id") <= 0 {
		return domains.Event{}, protocolError()
	}
	peer, err := publicPeer(asObject(m["peer_id"]))
	if err != nil {
		return domains.Event{}, err
	}
	peer = c.normalizedPeer(peer)
	id := strconv.FormatInt(m.num("id"), 10)
	date := m.num("date")
	if kind == "message.edited" && m.num("edit_date") > 0 {
		date = m.num("edit_date")
	}
	if date <= 0 {
		return domains.Event{}, protocolError()
	}
	sender := ""
	if from := asObject(m["from_id"]); from != nil {
		if p, e := publicPeer(from); e == nil {
			sender = p.ID
		}
	}
	out, _ := m["out"].(bool)
	direction := "incoming"
	if out {
		direction = "outgoing"
	}
	msg := &domains.Message{ID: id, ChatID: peer.ID, From: sender, SenderNameStatus: "unavailable", IsFromMe: &out, Timestamp: time.Unix(date, 0).UTC(), Body: m.str("message"), Kind: "text", Supported: m.str("_") == "message"}
	if kind == "message.edited" {
		msg.OriginalMessageID = id
	}
	if media := asObject(m["media"]); media != nil && media.str("_") != "messageMediaEmpty" {
		msg.Kind = "unsupported"
		msg.Supported = false
	}
	privateMedia, publicMedia := c.projectMedia(asObject(m["media"]))
	if publicMedia != nil {
		msg.Media = publicMedia
		msg.Kind = publicMedia.Type
		msg.Supported = true
	}
	if reply := asObject(m["reply_to"]); reply.num("reply_to_msg_id") > 0 {
		msg.RepliedToID = strconv.FormatInt(reply.num("reply_to_msg_id"), 10)
	}
	extra, err := structuredContent(asObject(m["media"]), msg)
	if err != nil {
		return domains.Event{}, err
	}
	content := object{"text": msg.Body, "supported": msg.Supported, "kind": msg.Kind}
	for key, value := range extra {
		content[key] = value
	}
	payload, _ := json.Marshal(content)
	identity := kind + "|" + peer.Key() + "|" + id
	if kind == "message.edited" {
		identity += "|" + strconv.FormatInt(date, 10) + "|" + string(payload)
	}
	return domains.Event{Provider: domains.ProviderEitaa, ID: eventHash(identity), Type: kind, Peer: peer, MessageID: id, SenderID: sender, Direction: direction, Time: msg.Timestamp, Payload: payload, Message: msg, Media: privateMedia}, nil
}

// Eitaa can emit the channel-side migration notice with id=0. It is a
// conversation transition, not an addressable message. Its own peer, source
// chat and provider date give it a stable identity without inventing a MID.
func (c *Client) projectMigrationNotice(m object) (domains.Event, error) {
	id, err := integer(m["id"])
	peer, peerErr := publicPeer(asObject(m["peer_id"]))
	source, sourceErr := integer(asObject(m["action"])["chat_id"])
	date := m.num("date")
	if err != nil || id != 0 || peerErr != nil || peer.Type != "channel" || sourceErr != nil || source <= 0 || date <= 0 {
		return domains.Event{}, protocolError()
	}
	peer = c.normalizedPeer(peer)
	sourceID := strconv.FormatInt(source, 10)
	payload, _ := json.Marshal(object{"provider_action": "messageActionChannelMigrateFrom", "source_chat_id": sourceID, "message_id_available": false})
	return domains.Event{Provider: domains.ProviderEitaa, ID: eventHash("chat.migrated|" + peer.Key() + "|" + sourceID + "|" + strconv.FormatInt(date, 10)), Type: "chat.migrated", Peer: peer, Direction: "unknown", Time: time.Unix(date, 0).UTC(), Payload: payload}, nil
}
func publicPeer(o object) (domains.Peer, error) {
	var kind, key string
	switch o.str("_") {
	case "peerUser":
		kind, key = "user", "user_id"
	case "peerChat":
		kind, key = "group", "chat_id"
	case "peerChannel":
		kind, key = "channel", "channel_id"
	default:
		return domains.Peer{}, protocolError()
	}
	id := o.num(key)
	if id <= 0 {
		return domains.Peer{}, protocolError()
	}
	return domains.Peer{Type: kind, ID: strconv.FormatInt(id, 10)}, nil
}

// A provider update's PTS is provenance for a distinct edit even when edit_date
// has only second resolution. It is not inferred from a diff page boundary.
func editUpdateID(event domains.Event, update object) string {
	return eventHash(event.ID + "|pts|" + strconv.FormatInt(update.num("pts"), 10))
}

// Only the individual wrapper's PTS orders media revisions. Full messages from
// history or difference.new_messages do not carry that provenance.
func applyMessageUpdateRevision(event *domains.Event, update object) error {
	scope := "account"
	switch update.str("_") {
	case "updateNewChannelMessage", "updateEditChannelMessage":
		rawPeer := asObject(asObject(update["message"])["peer_id"])
		if rawPeer.str("_") != "peerChannel" || strconv.FormatInt(rawPeer.num("channel_id"), 10) != peerWireID(event.Peer) {
			return protocolError()
		}
		scope = "channel:" + peerWireID(event.Peer)
	case "updateNewMessage", "updateEditMessage":
		if asObject(asObject(update["message"])["peer_id"]).str("_") == "peerChannel" {
			return protocolError()
		}
	default:
		return protocolError()
	}
	sequence := update.num("pts")
	if sequence < 0 || sequence > 2147483647 {
		return protocolError()
	}
	if sequence > 0 && event.MessageID != "" {
		event.MediaRevision = &domains.MediaRevision{Scope: scope, Sequence: sequence}
	}
	if event.Type == "message.edited" {
		event.ID = editUpdateID(*event, update)
	}
	return nil
}
