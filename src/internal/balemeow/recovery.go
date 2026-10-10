package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"strconv"
	"time"
)

const recoveryService = "bale.ghasedak.v1.GhasedakService"
const maxRoutes = 4096

type recoveryCheckpoint struct {
	Version int              `json:"version"`
	Account string           `json:"account"`
	Routes  map[string]int32 `json:"routes"`
	Gap     bool             `json:"gap,omitempty"`
}

func (c *Client) setRecovery(conn *connection, state, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == conn {
		c.status.Recovery = state
		if code != "" {
			c.status.LastError = code
		}
	}
}

// Only call after the recovery work/checkpoint has been accepted by the sink.
// A catch-up response can cover the arriving stream update itself, so that
// early-return path must finish the status transition as well.
func (c *Client) finishRecovery(conn *connection) {
	if conn.checkpoint.Gap || conn.recoveryFailed {
		c.setRecovery(conn, "gap_detected", "RECOVERY_GAP")
	} else if c.opts.RecoveryVerified {
		c.setRecovery(conn, "current", "")
	} else {
		c.setRecovery(conn, "degraded", "")
	}
}

func (c *Client) initializeRecovery(ctx context.Context, conn *connection) error {
	if c.opts.LoadCheckpoint == nil {
		return nil
	}
	c.setRecovery(conn, "recovering", "")
	raw, err := c.opts.LoadCheckpoint(ctx)
	if err != nil {
		return err
	}
	cp := recoveryCheckpoint{Version: 1, Account: conn.account, Routes: map[string]int32{}}
	if raw != "" {
		if len(raw) > 256<<10 || json.Unmarshal([]byte(raw), &cp) != nil || cp.Version != 1 || cp.Account != conn.account || cp.Routes == nil || len(cp.Routes) > maxRoutes {
			return boundedError("INVALID_CHECKPOINT", "stored recovery checkpoint is invalid for this account", 500)
		}
		for route, seq := range cp.Routes {
			if _, err := strconv.ParseUint(route, 10, 32); err != nil || seq < 0 {
				return updateFault("RECOVERY_RESPONSE_INVALID")
			}
		}
	}
	conn.checkpoint = cp
	conn.checkpointRaw = raw
	routes, err := c.discoverRecoveryRoutes(ctx)
	if err != nil {
		return err
	}
	for key, seq := range routes {
		if _, exists := conn.checkpoint.Routes[key]; exists {
			continue
		}
		if len(conn.checkpoint.Routes) >= maxRoutes {
			return updateFault("RECOVERY_RESPONSE_INVALID")
		}
		if raw != "" {
			// A conversation may have been joined while a gap was already present
			// or while this process was offline. Discover it without skipping to
			// today's provider snapshot. Existing durable cursors never move here.
			seq = 0
		}
		conn.checkpoint.Routes[key] = seq
	}
	if raw == "" {
		// First-attachment coverage starts at this snapshot. Persist every update
		// already admitted by the reader before recording its baseline marker.
		for {
			select {
			case payload, ok := <-conn.updates:
				if !ok {
					return unavailable()
				}
				conn.mu.Lock()
				conn.bufferedUpdateBytes -= int64(len(payload))
				conn.mu.Unlock()
				events, err := decodeStreamEvents(conn.account, payload)
				if err != nil {
					return err
				}
				for _, event := range events {
					if err := conn.sink(ctx, event); err != nil {
						return err
					}
				}
			default:
				goto initialDrained
			}
		}
	initialDrained:
		if err := c.persistRecovery(ctx, conn, nil, "starting_now"); err != nil {
			return err
		}
	} else if err := c.recoverRoutes(ctx, conn); err != nil {
		return err
	}
	c.finishRecovery(conn)
	return nil
}

func (c *Client) discoverRecoveryRoutes(ctx context.Context) (map[string]int32, error) {
	data, err := c.readRPC(ctx, recoveryService, "GetRoutesStates", &wire.RoutesRequest{})
	if err != nil {
		return nil, err
	}
	response := &wire.RoutesResponse{}
	if decode(data, response) != nil || len(response.States) == 0 || len(response.States) > maxRoutes {
		return nil, updateFault("RECOVERY_RESPONSE_INVALID")
	}
	routes := make(map[string]int32, len(response.States))
	for _, state := range response.States {
		if state.Group == nil || state.Sequence < 0 {
			return nil, updateFault("RECOVERY_RESPONSE_INVALID")
		}
		key := strconv.FormatUint(uint64(state.Group.Id), 10)
		if _, exists := routes[key]; exists {
			return nil, updateFault("RECOVERY_RESPONSE_INVALID")
		}
		routes[key] = state.Sequence
	}
	return routes, nil
}
func (c *Client) recoverRoutes(ctx context.Context, conn *connection) error {
	pending := map[string]int32{}
	for key, seq := range conn.checkpoint.Routes {
		pending[key] = seq
	}
	// Bounded total work prevents a large account or malformed provider response
	// from monopolizing a client forever. Remaining gaps stay visible.
	for page := 0; len(pending) > 0 && page < 100; page++ {
		states := []*wire.RouteState{}
		requested := map[string]int32{}
		for key, seq := range pending {
			if len(states) >= 64 {
				break
			}
			id, _ := strconv.ParseUint(key, 10, 32)
			states = append(states, &wire.RouteState{Group: &wire.PeerRef{Id: uint32(id)}, Sequence: seq})
			requested[key] = seq
		}
		data, err := c.readRPC(ctx, recoveryService, "GetDiff", &wire.DiffRequest{States: states, Optimizations: []int32{12}})
		if err != nil {
			return err
		}
		response := &wire.DiffResponse{}
		if decode(data, response) != nil || len(response.Routes) > len(states) || len(response.Users)+len(response.Groups) > maxRoutes {
			return updateFault("RECOVERY_RESPONSE_INVALID")
		}
		// References accompanying this page must be available before its events
		// are enriched and durably accepted. Validate all refs before caching any.
		for _, refs := range [][]*wire.PeerRef{response.Users, response.Groups} {
			for _, ref := range refs {
				if ref == nil || ref.Id == 0 {
					return updateFault("RECOVERY_RESPONSE_INVALID")
				}
			}
		}
		for _, ref := range response.Users {
			c.rememberRef("user", ref)
		}
		for _, ref := range response.Groups {
			c.rememberRef("group", ref)
		}
		seen := map[string]bool{}
		for _, diff := range response.Routes {
			if diff.State == nil || diff.State.Group == nil {
				return updateFault("RECOVERY_RESPONSE_INVALID")
			}
			key := strconv.FormatUint(uint64(diff.State.Group.Id), 10)
			previous, ok := requested[key]
			if !ok || seen[key] || diff.State.Sequence < previous {
				return updateFault("RECOVERY_RESPONSE_INVALID")
			}
			seen[key] = true
			if diff.GetTooLong().GetValue() {
				conn.checkpoint.Gap = true
				conn.recoveryFailed = true
				delete(pending, key)
				continue
			}
			if len(diff.Updates)+len(diff.DatedUpdates) > 4096 {
				return updateFault("RECOVERY_RESPONSE_INVALID")
			}
			items := diff.Updates
			if len(diff.DatedUpdates) > 0 {
				if len(items) > 0 {
					return updateFault("RECOVERY_RESPONSE_INVALID")
				}
				for _, u := range diff.DatedUpdates {
					items = append(items, u.Update)
				}
			}
			events := []domains.Event{}
			for itemIndex, item := range items {
				decoded, err := decodeEvents(conn.account, item)
				if err != nil {
					return err
				}
				if len(events)+len(decoded) > 4096 {
					return updateFault("RECOVERY_RESPONSE_INVALID")
				}
				position := key + "|diff-page:" + strconv.FormatInt(int64(diff.State.Sequence), 10) + ":" + strconv.Itoa(itemIndex)
				if len(diff.DatedUpdates) > 0 && diff.DatedUpdates[itemIndex].Date > 0 {
					position = key + "|date:" + sid(diff.DatedUpdates[itemIndex].Date)
				}
				stateEventsPosition(decoded, position)
				events = append(events, decoded...)
			}
			// The full page must commit before its reported route sequence advances.
			conn.checkpoint.Routes[key] = diff.State.Sequence
			if err := c.persistRecovery(ctx, conn, events, "catchup"); err != nil {
				conn.checkpoint.Routes[key] = previous
				return err
			}
			if diff.NeedMore {
				if diff.State.Sequence == previous {
					return updateFault("RECOVERY_RESPONSE_INVALID")
				}
				pending[key] = diff.State.Sequence
			} else {
				delete(pending, key)
			}
		}
		if len(seen) != len(requested) {
			return updateFault("RECOVERY_RESPONSE_INVALID")
		}
	}
	if len(pending) > 0 {
		conn.checkpoint.Gap = true
		conn.recoveryFailed = true
	}
	if conn.checkpoint.Gap {
		if err := c.persistRecovery(ctx, conn, nil, "gap_detected"); err != nil {
			return err
		}
	}
	return nil
}
func (c *Client) persistRecovery(ctx context.Context, conn *connection, events []domains.Event, phase string) error {
	cp, err := json.Marshal(conn.checkpoint)
	if err != nil {
		return err
	}
	if conn.batchSink != nil {
		payload, _ := json.Marshal(map[string]any{"phase": phase, "gap_detected": conn.checkpoint.Gap})
		marker := domains.Event{ID: eventHash(conn.account + "|checkpoint|" + string(cp)), Type: "connection.recovery", AccountID: conn.account, Direction: "unknown", Time: time.Now().UTC(), Payload: payload}
		batch := domains.EventBatch{Events: append(events, marker), Checkpoints: []domains.CheckpointTransition{{Scope: domains.DefaultCheckpointScope, Expected: conn.checkpointRaw, Next: string(cp)}}}
		if err := conn.batchSink(ctx, batch); err != nil {
			return err
		}
		conn.checkpointRaw = string(cp)
		return nil
	}
	// A separate checkpoint marker guarantees progress even if the last update
	// was already persisted before a crash; marker IDs change with the route map.
	for _, event := range events {
		if err := conn.sink(ctx, event); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(map[string]any{"phase": phase, "gap_detected": conn.checkpoint.Gap})
	event := domains.Event{ID: eventHash(conn.account + "|checkpoint|" + string(cp)), Type: "connection.recovery", AccountID: conn.account, Direction: "unknown", Time: time.Now().UTC(), Payload: payload, Checkpoint: string(cp)}
	return conn.sink(ctx, event)
}

func (c *Client) consumeRecoveredStream(ctx context.Context, conn *connection, data []byte) error {
	stream := &wire.StreamUpdate{}
	if decode(data, stream) != nil {
		return updateFault("RECOVERY_RESPONSE_INVALID")
	}
	if stream.Sequence < 0 || stream.RouteId < 0 {
		return updateFault("RECOVERY_STREAM_POSITION_INVALID")
	}
	if stream.Sequence == 0 || conn.recoveryFailed || conn.checkpoint.Gap {
		events, err := decodeStreamEvents(conn.account, data)
		if err != nil {
			return err
		}
		for _, e := range events {
			if err := conn.sink(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}
	key := strconv.FormatInt(int64(stream.RouteId), 10)
	previous, exists := conn.checkpoint.Routes[key]
	if exists && stream.Sequence <= previous {
		// A baseline/diff reply may overtake a live update in the transport. Deliver
		// it through durable dedupe instead of assuming its event is already stored.
		events, err := decodeStreamEvents(conn.account, data)
		if err != nil {
			return err
		}
		for _, event := range events {
			if err := conn.sink(ctx, event); err != nil {
				return err
			}
		}
		return nil
	}
	// When the stream jumps, recover from the durable cursor before admitting the
	// newer update. Unknown routes begin at zero, never at the arriving sequence.
	if !exists {
		if len(conn.checkpoint.Routes) >= maxRoutes {
			return updateFault("RECOVERY_RESPONSE_INVALID")
		}
		conn.checkpoint.Routes[key] = 0
		previous = 0
	}
	if stream.Sequence > previous+1 {
		c.setRecovery(conn, "recovering", "")
		if err := c.recoverRoutes(ctx, conn); err != nil {
			return err
		}
		previous = conn.checkpoint.Routes[key]
		if stream.Sequence <= previous {
			c.finishRecovery(conn)
			return nil
		}
		if stream.Sequence > previous+1 {
			conn.checkpoint.Gap = true
			conn.recoveryFailed = true
			// Record the unresolved hole before accepting any newer live message.
			// Otherwise a restart reloads the old gap-free cursor and can advertise
			// current even when GetDiff still has not accounted for the jump.
			if err := c.persistRecovery(ctx, conn, nil, "gap_detected"); err != nil {
				return err
			}
		}
	}
	events, err := decodeStreamEvents(conn.account, data)
	if err != nil {
		return err
	}
	if conn.recoveryFailed || conn.checkpoint.Gap {
		// Continue delivering newly observed messages, but never paper over a hole
		// by replacing the last known contiguous recovery position.
		for _, e := range events {
			if err := conn.sink(ctx, e); err != nil {
				return err
			}
		}
		c.setRecovery(conn, "gap_detected", "RECOVERY_GAP")
		return nil
	}
	conn.checkpoint.Routes[key] = stream.Sequence
	if err := c.persistRecovery(ctx, conn, events, "stream"); err != nil {
		conn.checkpoint.Routes[key] = previous
		return err
	}
	c.finishRecovery(conn)
	return nil
}

// A schema we cannot interpret stops checkpoint progress, not unrelated live
// traffic. Keep the last contiguous cursor and a durable gap marker. No raw
// protocol bytes are persisted or exposed.
func (c *Client) recordProtocolGap(ctx context.Context, conn *connection, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	conn.checkpoint.Gap = true
	conn.recoveryFailed = true
	events := []domains.Event{}
	if len(data) != 0 {
		events = append(events, domains.Event{ID: eventHash(conn.account + "|invalid_stream|" + string(data)), Type: "protocol.unsupported_update", AccountID: conn.account, Direction: "unknown", Time: time.Now().UTC(), Payload: json.RawMessage(`{"supported":false,"gap_detected":true}`)})
	}
	if err := c.persistRecovery(ctx, conn, events, "gap_detected"); err != nil {
		return err
	}
	c.setRecovery(conn, "gap_detected", "RECOVERY_GAP")
	return nil
}
