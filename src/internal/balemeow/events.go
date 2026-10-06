package balemeow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

func (c *Client) consumeUpdates(conn *connection) {
	// One total post-close deadline bounds all admitted updates, including a
	// sink call that was already executing when the socket closed.
	drainCtx, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	go func() {
		select {
		case <-conn.ctx.Done():
		case <-drainCtx.Done():
			return
		}
		timer := time.NewTimer(c.opts.DrainTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancelDrain()
		case <-drainCtx.Done():
		}
	}()
	if c.opts.LoadCheckpoint != nil {
		if err := c.initializeRecovery(conn.ctx, conn); err != nil {
			c.reportDiagnostic(diagnosticCode(err, "RECOVERY_INITIAL_FAILED"))
			if isProtocolFault(err) && conn.checkpoint.Routes != nil {
				if err := c.recordProtocolGap(drainCtx, conn, nil); err != nil {
					c.fail(conn, "EVENT_PERSIST_FAILED", false)
					return
				}
			} else {
				c.setRecovery(conn, "gap_detected", "RECOVERY_FAILED")
				c.fail(conn, "RECOVERY_FAILED", false)
				return
			}
		}
	}
	for {
		if drainCtx.Err() != nil {
			c.recordDrainFailure(conn)
			return
		}
		var data []byte
		select {
		case payload, ok := <-conn.updates:
			if !ok {
				return
			}
			data = payload
		case <-drainCtx.Done():
			c.recordDrainFailure(conn)
			return
		}
		conn.mu.Lock()
		conn.bufferedUpdateBytes -= int64(len(data))
		conn.mu.Unlock()
		sink, account := conn.sink, conn.account
		if sink == nil || account == "" {
			c.fail(conn, "EVENT_CONTEXT_MISSING", false)
			return
		}
		if c.opts.LoadCheckpoint != nil {
			ctx, cancel := context.WithTimeout(drainCtx, c.opts.RequestTimeout)
			err := c.consumeRecoveredStream(ctx, conn, data)
			cancel()
			if err != nil {
				c.reportDiagnostic(diagnosticCode(err, "RECOVERY_STREAM_FAILED"))
				if isProtocolFault(err) {
					if err := c.recordProtocolGap(drainCtx, conn, data); err != nil {
						c.fail(conn, "EVENT_PERSIST_FAILED", false)
						return
					}
					continue
				}
				c.setRecovery(conn, "gap_detected", "RECOVERY_FAILED")
				c.fail(conn, "RECOVERY_FAILED", false)
				return
			}
			continue
		}
		events, err := decodeStreamEvents(account, data)
		if err != nil {
			c.fail(conn, "UPDATE_PROTOCOL_ERROR", false)
			return
		}
		for _, event := range events {
			ctx, cancel := context.WithTimeout(drainCtx, c.opts.RequestTimeout)
			err = sink(ctx, event)
			cancel()
			if err != nil {
				if conn.ctx.Err() != nil {
					c.recordDrainFailure(conn)
				}
				c.fail(conn, "EVENT_PERSIST_FAILED", false)
				return
			}
		}
	}
}

func decodeEvents(account string, data []byte) ([]domains.Event, error) {
	update := &wire.UpdateContainer{}
	if decode(data, update) != nil {
		return nil, updateFault("UPDATE_UNION_DECODE_FAILED")
	}
	if deleted := update.Deleted; deleted != nil && len(deleted.Rids) > 4096 {
		return nil, updateFault("UPDATE_EXPANSION_LIMIT")
	}
	for _, message := range []*wire.Message{update.GetMessage().GetMessage(), update.GetEdited().GetMessage()} {
		if err := validateMessage(message); err != nil {
			return nil, updateFault("UPDATE_DOCUMENT_INVALID")
		}
	}
	groupEvents, groupErr := groupEvents(account, data)
	if groupErr != nil {
		return nil, updateFault("UPDATE_GROUP_INVALID")
	}
	events, extraErr := additionalEvents(account, data, update)
	if extraErr != nil {
		return nil, updateFault("UPDATE_EXTENDED_INVALID")
	}
	events = append(events, groupEvents...)
	presence, presenceErr := presenceEvents(account, data)
	if presenceErr != nil {
		return nil, updateFault("UPDATE_PRESENCE_INVALID")
	}
	events = append(events, presence...)
	if msg := update.Message; msg != nil {
		peer, err := decodePeer(msg.Peer)
		if err != nil || msg.Rid == 0 || msg.Date <= 0 || validateQuote(msg.QuotedMessage) != nil {
			return nil, updateFault("UPDATE_MESSAGE_INVALID")
		}
		if msg.ExPeer != nil {
			ex, exErr := safeExPeer(msg.ExPeer)
			if exErr != nil || ex.ID != peer.ID || (peer.Type == "user" && ex.Type != "user") || (peer.Type == "group" && ex.Type != "group" && ex.Type != "channel") {
				return nil, updateFault("UPDATE_MESSAGE_INVALID")
			}
			peer = ex
		}
		body := decoratedPayload(msg.Message, msg.QuotedMessage, msg.Previous, msg.Thread, msg.GroupedId, msg.AuthorSign)
		sender := strconv.FormatUint(uint64(msg.SenderId), 10)
		direction := "incoming"
		if sender == account {
			direction = "outgoing"
		}
		event := domains.Event{Type: "message", AccountID: account, Peer: peer, MessageID: strconv.FormatInt(msg.Rid, 10), SenderID: sender, Direction: direction, Time: time.UnixMilli(msg.Date).UTC(), Payload: body, Media: providerMedia(msg.Message)}
		event.ID = eventHash(account + "|message|" + messageIdentityPeer(peer) + "|" + event.MessageID)
		events = append(events, event)
	}
	if edit := update.Edited; edit != nil {
		peer, err := decodePeer(edit.Peer)
		if err != nil || edit.Rid == 0 || edit.GetDate().GetValue() <= 0 {
			return nil, updateFault("UPDATE_EDIT_INVALID")
		}
		body := messagePayload(edit.Message)
		sender := strconv.FormatUint(uint64(edit.GetUpdaterUserId().GetValue()), 10)
		direction := "incoming"
		if sender == account {
			direction = "outgoing"
		}
		event := domains.Event{Type: "message.edited", AccountID: account, Peer: peer, MessageID: strconv.FormatInt(edit.Rid, 10), SenderID: sender, Direction: direction, Time: time.UnixMilli(edit.GetDate().GetValue()).UTC(), Payload: body, Media: providerMedia(edit.Message)}
		event.ID = eventHash(account + "|edit|" + peer.Key() + "|" + event.MessageID + "|" + strconv.FormatInt(edit.GetDate().GetValue(), 10) + "|" + string(body))
		events = append(events, event)
	}
	if deleted := update.Deleted; deleted != nil {
		peer, err := decodePeer(deleted.Peer)
		if err != nil {
			return nil, updateFault("UPDATE_DELETE_PEER_INVALID")
		}
		for _, rid := range deleted.Rids {
			if rid == 0 {
				return nil, updateFault("UPDATE_DELETE_RID_INVALID")
			}
			event := domains.Event{Type: "message.deleted", AccountID: account, Peer: peer, MessageID: strconv.FormatInt(rid, 10), Direction: "unknown", Time: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
			event.ID = eventHash(account + "|delete|" + peer.Key() + "|" + event.MessageID)
			events = append(events, event)
		}
	}
	if sent := update.Sent; sent != nil {
		peer, err := decodePeer(sent.Peer)
		if err != nil || sent.Rid == 0 || sent.Date <= 0 {
			return nil, updateFault("UPDATE_ACCEPTED_INVALID")
		}
		event := domains.Event{Type: "message.accepted", AccountID: account, Peer: peer, MessageID: strconv.FormatInt(sent.Rid, 10), Direction: "outgoing", Time: time.UnixMilli(sent.Date).UTC(), Payload: json.RawMessage(`{"accepted":true}`)}
		event.ID = eventHash(account + "|accepted|" + peer.Key() + "|" + event.MessageID)
		events = append(events, event)
	}
	if len(events) > 4096 {
		return nil, updateFault("UPDATE_EXPANSION_LIMIT")
	}
	if len(events) == 0 {
		// Unknown updates are observable but do not expose opaque binary data (which
		// may contain new credential fields). Their fingerprint is stable for replay.
		id := eventHash(account + "|unsupported|" + string(data))
		events = append(events, domains.Event{ID: id, Type: "protocol.unsupported_update", AccountID: account, Direction: "unknown", Time: time.Now().UTC(), Payload: json.RawMessage(`{"supported":false}`)})
	}
	return events, nil
}
func eventHash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func decodePeer(p *wire.Peer) (domains.Peer, error) {
	if p == nil || p.Id == 0 {
		return domains.Peer{}, protocolError()
	}
	kind := ""
	switch p.Type {
	case 1:
		kind = "user"
	case 2:
		kind = "group"
	default:
		return domains.Peer{}, protocolError()
	}
	return domains.Peer{Type: kind, ID: strconv.FormatUint(uint64(p.Id), 10)}, nil
}

// Preserve visibility of admitted updates that could not commit during closure;
// one-time transport cleanup must not erase this later storage failure.
func (c *Client) recordDrainFailure(conn *connection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastConn == conn {
		c.status.LastError = "EVENT_DRAIN_INCOMPLETE"
		c.status.Recovery = "gap_detected"
	}
}

// Stream framing is separate from the update union. The current official web
// client decodes MavizStream.SubscribeToUpdates payloads before dispatching unions.
func decodeStreamEvents(account string, data []byte) ([]domains.Event, error) {
	stream := &wire.StreamUpdate{}
	if decode(data, stream) != nil {
		return nil, updateFault("UPDATE_STREAM_DECODE_FAILED")
	}
	items := [][]byte{}
	if len(stream.Update) > 0 {
		items = append(items, stream.Update)
	}
	if stream.Updates != nil {
		items = append(items, stream.Updates.Updates...)
	}
	if len(items) > 4096 {
		return nil, updateFault("UPDATE_EXPANSION_LIMIT")
	}
	if len(items) == 0 {
		return []domains.Event{{ID: eventHash(account + "|unsupported_stream|" + string(data)), Type: "protocol.unsupported_update", AccountID: account, Direction: "unknown", Time: time.Now().UTC(), Payload: json.RawMessage(`{"supported":false}`)}}, nil
	}
	events := []domains.Event{}
	for _, item := range items {
		decoded, err := decodeEvents(account, item)
		if err != nil {
			return nil, err
		}
		if len(events)+len(decoded) > 4096 {
			return nil, updateFault("UPDATE_EXPANSION_LIMIT")
		}
		events = append(events, decoded...)
	}
	stateEventsPosition(events, streamPosition(stream.RouteId, stream.Sequence, stream.Timestamp))
	// Sequence is deliberately not checkpointed until recovery is verified.
	return events, nil
}
