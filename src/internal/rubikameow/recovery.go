package rubikameow

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
)

const accountScope = "rubika.chats"

type pendingChat struct {
	GUID        string `json:"guid"`
	LastMessage string `json:"last_message,omitempty"`
}
type checkpoint struct {
	Version      int           `json:"version"`
	Account      string        `json:"account"`
	Peer         string        `json:"peer,omitempty"`
	State        string        `json:"state"`
	Listing      bool          `json:"listing,omitempty"`
	RefreshPeers bool          `json:"refresh_peers,omitempty"`
	Page         string        `json:"page,omitempty"`
	Gap          bool          `json:"gap,omitempty"`
	GapReason    string        `json:"gap_reason,omitempty"`
	Pending      []pendingChat `json:"pending,omitempty"`
	LastMessage  string        `json:"last_message,omitempty"`
}

var errMoreMessages = errors.New("another bounded message page is pending")

func eventHash(s string) string           { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func checkpointJSON(cp checkpoint) string { raw, _ := json.Marshal(cp); return string(raw) }
func parseCheckpoint(raw, account string) (checkpoint, error) {
	cp := checkpoint{}
	if raw == "" {
		return checkpoint{Version: 2, Account: account, Listing: true}, nil
	}
	if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &cp) != nil || (cp.Version != 1 && cp.Version != 2) || cp.Account != account || len(cp.State) > 256 || len(cp.Page) > 256 || len(cp.Pending) > 4000 {
		return cp, domains.E("INVALID_CHECKPOINT", "stored Rubika checkpoint is invalid", 500)
	}
	if cp.Version == 1 {
		// The old flag conflated skipped Bot/Service chats with OldState. Preserve
		// that uncertainty explicitly; neither clear it nor assert proven loss.
		if cp.Gap {
			cp.GapReason = "legacy_unclassified"
		}
		if cp.Peer == "" && !cp.Listing {
			// Discover previously skipped conversations without replacing the
			// durable account cursor with the newer listing snapshot.
			cp.RefreshPeers = true
			cp.Page = ""
		}
		cp.Version = 2
	}
	if (cp.Gap && cp.GapReason != "legacy_unclassified" && cp.GapReason != "chat_state_expired" && cp.GapReason != "message_state_expired") || (!cp.Gap && cp.GapReason != "") {
		return cp, domains.E("INVALID_CHECKPOINT", "stored Rubika gap provenance is invalid", 500)
	}
	if cp.LastMessage != "" && !validMessageID(cp.LastMessage) {
		return cp, domains.E("INVALID_CHECKPOINT", "stored Rubika message cursor is invalid", 500)
	}
	for _, p := range cp.Pending {
		if !guidPattern.MatchString(p.GUID) {
			return cp, domains.E("INVALID_CHECKPOINT", "stored Rubika peer checkpoint is invalid", 500)
		}
	}
	return cp, nil
}
func stateValue(state string) any {
	if n, e := strconv.ParseInt(state, 10, 64); e == nil {
		return json.Number(strconv.FormatInt(n, 10))
	}
	return state
}
func validState(state string) bool { return domains.ValidOpaqueID(state) }
func (c *Client) Connect(context.Context, *domains.Session, domains.Sink) error {
	return domains.E("BATCH_SINK_REQUIRED", "Rubika requires a durable batch sink", 500)
}
func (c *Client) ConnectBatch(ctx context.Context, s *domains.Session, sink domains.BatchSink) (retErr error) {
	defer func() {
		if retErr != nil {
			auth, code := "", "CONNECTION_FAILED"
			var de *domains.Error
			if errors.As(retErr, &de) && de.Code == "AUTH_REQUIRED" {
				auth, code = "auth_required", "AUTH_REQUIRED"
			}
			c.setStatus(auth, "disconnected", "degraded", code)
		}
	}()
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	if sink == nil || c.cfg.LoadCheckpoint == nil {
		return domains.E("BATCH_SINK_REQUIRED", "Rubika requires durable update callbacks", 500)
	}
	ps, e := decodeSession(s)
	if e != nil {
		return e
	}
	if e = c.Disconnect(ctx); e != nil {
		return e
	}
	key, e := parsePrivateKey(ps.PrivateKey)
	if e != nil {
		return protocolError()
	}
	c.mu.Lock()
	c.session = ps
	c.key = key
	c.mu.Unlock()
	c.setStatus("authenticated", "connecting", "recovering", "")
	profile, e := c.invoke(ctx, "getUserInfo", object{"user_guid": ps.UserID}, false, "")
	var registrationError *domains.Error
	if errors.As(e, &registrationError) && registrationError.Code == "DEVICE_REGISTRATION_REQUIRED" {
		// NOT_REGISTERED describes this client device, not an invalid account
		// credential. The saved signing key gives registration a stable identity.
		// On an interrupted registration, the next connection first checks the
		// profile again; it never repeats registration after a successful check.
		public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return protocolError()
		}
		hash := sha256.Sum256(public)
		_, e = c.invoke(ctx, "registerDevice", object{
			"token_type": "Web", "token": "", "lang_code": "fa",
			"app_version":    "PW_" + clientIdentity().str("app_version"),
			"system_version": runtime.GOOS, "device_model": "GoOmni",
			"device_hash": hex.EncodeToString(hash[:]),
		}, true, "")
		if e != nil {
			return e
		}
		profile, e = c.invoke(ctx, "getUserInfo", object{"user_guid": ps.UserID}, false, "")
	}
	if e != nil {
		return e
	}
	if asObject(profile["user"]).str("user_guid") != ps.UserID {
		return domains.E("ACCOUNT_CHANGED", "Rubika session changed account", 409)
	}
	raw, e := c.cfg.LoadCheckpoint(ctx, accountScope)
	if e != nil {
		return e
	}
	cp, e := parseCheckpoint(raw, ps.UserID)
	if e == nil && cp.Peer != "" {
		e = domains.E("INVALID_CHECKPOINT", "account checkpoint contains a peer scope", 500)
	}
	if e != nil {
		return e
	}
	// Durable gap evidence must remain visible even if dialing fails. Loading
	// it cannot imply that the transport is connected.
	c.mu.Lock()
	c.status.RecoveryIssue = cp.GapReason
	if cp.Gap && cp.GapReason != "legacy_unclassified" {
		c.status.Recovery = "gap_detected"
	}
	c.mu.Unlock()
	c.mu.RLock()
	socketURL := c.socket
	c.mu.RUnlock()
	// The connection owns its lifetime after the request completes. The gateway
	// cancels it through Disconnect on shutdown/logout; request timeout only
	// bounds dialing and initial validation.
	conn, _, e := websocket.Dial(ctx, socketURL, &websocket.DialOptions{HTTPClient: c.http})
	if e != nil {
		return domains.E("PROVIDER_TRANSPORT_ERROR", "Rubika socket connection failed", 502)
	}
	conn.SetReadLimit(maxPayload)
	handshake, _ := json.Marshal(object{"api_version": "6", "auth": ps.Auth, "method": "handShake", "data": ""})
	if e = conn.Write(ctx, websocket.MessageText, handshake); e != nil {
		conn.CloseNow()
		return domains.E("PROVIDER_TRANSPORT_ERROR", "Rubika socket handshake failed", 502)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.mu.Lock()
	c.cancel = cancel
	c.done = done
	c.mu.Unlock()
	c.setStatus("authenticated", "connected", "degraded", "")
	aesKey, _ := authKey(ps.Auth)
	go func() {
		defer close(done)
		defer conn.CloseNow()
		defer cancel()
		var workers sync.WaitGroup
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTimer(0)
			defer ticker.Stop()
			failures := 0
			nextPoll := time.Time{}
			nextHeartbeat := time.Time{}
			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
				}
				ticker.Reset(pollJitter(time.Second))
				if !time.Now().Before(nextHeartbeat) {
					if e := conn.Write(runCtx, websocket.MessageText, []byte(`{}`)); e != nil {
						cancel()
						return
					}
					nextHeartbeat = time.Now().Add(pollJitter(10 * time.Second))
				}
				if time.Now().Before(nextPoll) {
					continue
				}
				next, e := c.recoverPage(runCtx, raw, cp, sink)
				if e != nil {
					var de *domains.Error
					if errors.As(e, &de) && de.Code == "AUTH_REQUIRED" {
						c.setStatus("auth_required", "disconnected", "degraded", "AUTH_REQUIRED")
						cancel()
						return
					}
					failures = min(failures+1, 3)
					nextPoll = time.Now().Add(pollJitter(min(10*time.Second*time.Duration(1<<failures), 60*time.Second)))
					c.setStatus("", "connected", "degraded", "RECOVERY_FAILED")
					continue
				}
				failures = 0
				delay := 10 * time.Second
				if next.Listing || next.RefreshPeers || len(next.Pending) > 0 {
					delay = time.Second
				}
				nextPoll = time.Now().Add(pollJitter(delay))
				raw = checkpointJSON(next)
				cp = next
				c.setRecoveryStatus(cp)
			}
		}()
		code := "CONNECTION_LOST"
		for {
			_, frame, e := conn.Read(runCtx)
			if e != nil {
				break
			}
			if e = c.acceptFrame(runCtx, frame, aesKey, sink); e != nil {
				code = "UPDATE_ACCEPTANCE_FAILED"
				break
			}
		}
		cancel()
		conn.CloseNow()
		workers.Wait()
		if c.Status().Auth == "auth_required" {
			code = "AUTH_REQUIRED"
		}
		c.setStatus("", "disconnected", "degraded", code)
	}()
	return nil
}
func (c *Client) recoverPage(ctx context.Context, expected string, cp checkpoint, sink domains.BatchSink) (checkpoint, error) {
	if c.cfg.PollPermit != nil {
		release, e := c.cfg.PollPermit(ctx)
		if e != nil {
			return cp, e
		}
		defer release()
	}
	next := cp
	next.Pending = append([]pendingChat(nil), cp.Pending...)
	if len(cp.Pending) > 0 {
		// Rotate a peer with another bounded page behind its neighbours. A busy
		// conversation must not monopolize account recovery after an outage.
		remaining := append([]pendingChat(nil), cp.Pending[min(4, len(cp.Pending)):]...)
		for i := 0; i < min(4, len(cp.Pending)); i++ {
			gap, e := c.recoverPeer(ctx, cp.Pending[i], sink)
			if errors.Is(e, errMoreMessages) {
				remaining = append(remaining, cp.Pending[i])
				continue
			}
			if e != nil {
				return cp, e
			}
			if gap {
				next.Gap = true
				next.GapReason = "message_state_expired"
			}
		}
		next.Pending = remaining
		if e := sink(ctx, domains.EventBatch{Checkpoints: []domains.CheckpointTransition{{Scope: accountScope, Expected: expected, Next: checkpointJSON(next)}}}); e != nil {
			return cp, e
		}
		return next, nil
	}
	method := "getChatsUpdates"
	input := object{"state": stateValue(cp.State)}
	if cp.Listing || cp.RefreshPeers {
		method = "getChats"
		input = object{}
		if cp.Page != "" {
			input["start_id"] = cp.Page
		}
	}
	o, e := c.invoke(ctx, method, input, false, "")
	if e != nil {
		return cp, e
	}
	events := []domains.Event{}
	if o.str("status") == "OldState" {
		next.Gap = true
		next.GapReason = "chat_state_expired"
		next.RefreshPeers = false
		next.Listing = true
		next.Page = ""
		next.State = ""
		events = append(events, recoveryEvent(cp.Account, "old_chat_state", expected))
	} else {
		state := o.str("new_state")
		if cp.RefreshPeers {
			state = cp.State
		}
		if cp.Listing {
			state = o.str("state")
			if cp.Page != "" {
				state = cp.State
			}
		}
		if !validState(state) {
			return cp, protocolError()
		}
		next.State = state
		chats, ok := o["chats"].([]any)
		if !ok || len(chats) > 1000 {
			return cp, protocolError()
		}
		for _, value := range chats {
			chat := asObject(value)
			guid := chat.str("object_guid")
			if !guidPattern.MatchString(guid) {
				events = append(events, unsupportedPeerEvent(cp.Account, guid, "chat", state))
				continue
			}
			next.Pending = append(next.Pending, pendingChat{GUID: guid, LastMessage: chat.str("last_message_id")})
			payload, _ := json.Marshal(publicObject(chat))
			events = append(events, domains.Event{Provider: domains.ProviderRubika, ID: eventHash("chat|" + state + "|" + guid), Type: "chat.updated", Peer: peerFromGUID(guid), Direction: "unknown", Time: time.Now().UTC(), Payload: payload})
		}
		if cp.Listing || cp.RefreshPeers {
			has, _ := o["has_continue"].(bool)
			if cp.RefreshPeers {
				next.RefreshPeers = has
			} else {
				next.Listing = has
			}
			next.Page = o.str("next_start_id")
			if has && (len(chats) == 0 || next.Page == "" || next.Page == cp.Page) {
				return cp, protocolError()
			}
			if cp.State == "" {
				events = append(events, recoveryEvent(cp.Account, "starting_now", state))
			}
		}
		if deleted, ok := o["deleted_chats"].([]any); ok {
			if len(deleted) > 1000 {
				return cp, protocolError()
			}
			for _, v := range deleted {
				guid, ok := v.(string)
				if !ok {
					return cp, protocolError()
				}
				if !guidPattern.MatchString(guid) {
					events = append(events, unsupportedPeerEvent(cp.Account, guid, "chat_deleted", state))
					continue
				}
				events = append(events, domains.Event{Provider: domains.ProviderRubika, ID: eventHash("chat_delete|" + state + "|" + guid), Type: "chat.deleted", Peer: peerFromGUID(guid), Direction: "unknown", Time: time.Now().UTC(), Payload: json.RawMessage(`{"timestamp_source":"observed"}`)})
			}
		}
	}
	if len(events) > 1000 {
		return cp, protocolError()
	}
	if e = sink(ctx, domains.EventBatch{Events: events, Checkpoints: []domains.CheckpointTransition{{Scope: accountScope, Expected: expected, Next: checkpointJSON(next)}}}); e != nil {
		return cp, e
	}
	c.observeCoverage(events)
	return next, nil
}
func (c *Client) recoverPeer(ctx context.Context, p pendingChat, sink domains.BatchSink) (bool, error) {
	scope := "rubika.messages:" + p.GUID
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	raw, e := c.cfg.LoadCheckpoint(ctx, scope)
	if e != nil {
		return false, e
	}
	cp, e := parseCheckpoint(raw, account)
	if e != nil {
		return false, e
	}
	if raw != "" && cp.Peer != p.GUID {
		return false, domains.E("INVALID_CHECKPOINT", "message checkpoint belongs to another peer", 500)
	}
	cp.Peer = p.GUID
	next := cp
	next.Listing = false
	events := []domains.Event{}
	if raw == "" || cp.State == "" {
		if !validMessageID(p.LastMessage) {
			return cp.Gap && cp.GapReason != "legacy_unclassified", nil
		}
		o, e := c.invoke(ctx, "getMessagesInterval", object{"object_guid": p.GUID, "middle_message_id": p.LastMessage}, false, "")
		if e != nil {
			return false, e
		}
		next.State = o.str("state")
		if !validState(next.State) {
			return false, protocolError()
		}
		// The interval response is not just a state token. It may contain the
		// first messages of a conversation discovered while disconnected. Accept
		// that bounded window and its cursor together before advancing the chat.
		messages, ok := o["messages"].([]any)
		if !ok || len(messages) > 1000 {
			return false, protocolError()
		}
		maxID := int64(0)
		for _, value := range messages {
			message := asObject(value)
			if guid := message.str("object_guid"); guid != "" && guid != p.GUID {
				return false, protocolError()
			}
			id := message.str("message_id")
			if !validMessageID(id) {
				return false, protocolError()
			}
			event, err := c.projectMessage(p.GUID, object{"object_guid": p.GUID, "action": "New", "message_id": id, "message": message})
			if err != nil {
				return false, err
			}
			events = append(events, event)
			n, _ := strconv.ParseInt(id, 10, 64)
			maxID = max(maxID, n)
		}
		if len(events) >= 1000 {
			return false, protocolError()
		}
		// A fresh mutation baseline after OldState must not jump the independent
		// new-message watermark to this latest bounded interval. Preserve the
		// last durable ID so FromMin can drain every observable later page.
		if maxID > 0 && cp.LastMessage == "" {
			next.LastMessage = strconv.FormatInt(maxID, 10)
		}
		events = append(events, recoveryEvent(account, "conversation_starting_now", p.GUID+"|"+next.State))
	} else {
		o, e := c.invoke(ctx, "getMessagesUpdates", object{"object_guid": p.GUID, "state": stateValue(cp.State)}, false, "")
		if e != nil {
			return false, e
		}
		if o.str("status") == "OldState" {
			next.Gap = true
			next.GapReason = "message_state_expired"
			next.State = ""
			events = append(events, recoveryEvent(account, "old_message_state", p.GUID+"|"+raw))
		} else {
			next.State = o.str("new_state")
			if !validState(next.State) {
				return false, protocolError()
			}
			updates, ok := o["updated_messages"].([]any)
			if !ok || len(updates) > 1000 {
				return false, protocolError()
			}
			for _, v := range updates {
				event, e := c.projectMessage(p.GUID, asObject(v))
				if e != nil {
					return false, e
				}
				events = append(events, event)
			}
		}
	}
	if e := sink(ctx, domains.EventBatch{Events: events, Checkpoints: []domains.CheckpointTransition{{Scope: scope, Expected: raw, Next: checkpointJSON(next)}}}); e != nil {
		return false, e
	}
	c.observeCoverage(events)
	gap := next.Gap && next.GapReason != "legacy_unclassified"
	// A retained historical gap does not disable recovery of later new
	// messages. Only the just-expired mutation state needs a new baseline first.
	if raw != "" && next.State != "" && validMessageID(p.LastMessage) {
		if err := c.recoverNewMessages(ctx, p, next, sink); err != nil {
			return gap, err
		}
	}
	return gap, nil
}

// Rubika's mutation state covers edits/deletes; it is not a cursor for newly
// created messages. Read new IDs separately using the reviewed FromMin path.
func (c *Client) recoverNewMessages(ctx context.Context, p pendingChat, cp checkpoint, sink domains.BatchSink) error {
	last, _ := strconv.ParseInt(cp.LastMessage, 10, 64)
	target, _ := strconv.ParseInt(p.LastMessage, 10, 64)
	if last >= target {
		return nil
	}
	method := "getMessages"
	input := object{"object_guid": p.GUID, "sort": "FromMin", "min_id": strconv.FormatInt(last+1, 10), "limit": 100}
	if cp.LastMessage == "" {
		// Older checkpoints have no new-message watermark. A bounded snapshot
		// recovers observable messages without claiming an exhaustive import.
		method = "getMessagesInterval"
		input = object{"object_guid": p.GUID, "middle_message_id": p.LastMessage}
	}
	o, err := c.invoke(ctx, method, input, false, "")
	if err != nil {
		return err
	}
	messages, ok := o["messages"].([]any)
	if !ok || len(messages) > 1000 {
		return protocolError()
	}
	events := make([]domains.Event, 0, len(messages)+1)
	maxID := last
	for _, value := range messages {
		message := asObject(value)
		if guid := message.str("object_guid"); guid != "" && guid != p.GUID {
			return protocolError()
		}
		id := message.str("message_id")
		if !validMessageID(id) {
			return protocolError()
		}
		n, _ := strconv.ParseInt(id, 10, 64)
		if n <= last {
			continue // An inclusive provider boundary cannot rewind the watermark.
		}
		event, err := c.projectMessage(p.GUID, object{"object_guid": p.GUID, "action": "New", "message_id": id, "message": message})
		if err != nil {
			return err
		}
		events = append(events, event)
		maxID = max(maxID, n)
	}
	hasMore := false
	if method == "getMessages" {
		var valid bool
		hasMore, valid = o["has_continue"].(bool)
		if !valid || (hasMore && maxID == last) {
			return protocolError()
		}
	} else {
		events = append(events, recoveryEvent(cp.Account, "message_cursor_baseline_window", p.GUID+"|"+p.LastMessage))
		if maxID == last {
			return protocolError()
		}
	}
	if maxID == last {
		return nil
	}
	next := cp
	next.LastMessage = strconv.FormatInt(maxID, 10)
	if err = sink(ctx, domains.EventBatch{Events: events, Checkpoints: []domains.CheckpointTransition{{Scope: "rubika.messages:" + p.GUID, Expected: checkpointJSON(cp), Next: checkpointJSON(next)}}}); err != nil {
		return err
	}
	c.observeCoverage(events)
	if hasMore {
		return errMoreMessages
	}
	return nil
}
func recoveryEvent(account, phase, provenance string) domains.Event {
	data, _ := json.Marshal(object{"phase": phase, "initial_history_imported": false, "timestamp_source": "observed"})
	return domains.Event{Provider: domains.ProviderRubika, ID: eventHash(account + "|recovery|" + phase + "|" + provenance), Type: "connection.recovery", Direction: "unknown", Time: time.Now().UTC(), Payload: data}
}

// Unrecognized namespaces remain explicit coverage diagnostics, separate from
// provider-confirmed expired recovery states. Never reinterpret them as users.
func unsupportedPeerEvent(account, guid, action, provenance string) domains.Event {
	kind := "unknown"
	payload, _ := json.Marshal(object{"supported": false, "peer_kind": kind, "action": action})
	return domains.Event{Provider: domains.ProviderRubika, ID: eventHash(account + "|unsupported_peer|" + guid + "|" + action + "|" + provenance), Type: "protocol.unsupported_update", Direction: "unknown", Time: time.Now().UTC(), Payload: payload}
}
func (c *Client) acceptFrame(ctx context.Context, raw, key []byte, sink domains.BatchSink) error {
	envelope, e := jsonObject(raw)
	if e != nil {
		return protocolError()
	}
	// The reviewed web client treats envelopes without a type as socket pongs.
	// They carry no durable progress and cannot reconcile writes.
	if envelope.str("type") == "" && envelope.str("data_enc") == "" {
		return nil
	}
	if envelope.str("type") != "messenger" {
		return protocolError()
	}
	encrypted := envelope.str("data_enc")
	if encrypted == "" {
		return protocolError()
	}
	frame, e := decrypt(encrypted, key)
	if e != nil {
		return protocolError()
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	if user := frame.str("user_guid"); user != "" && user != account {
		return domains.E("ACCOUNT_CHANGED", "Rubika update belongs to another account", 409)
	}
	events := []domains.Event{}
	updates, ok := frame["message_updates"].([]any)
	if ok {
		if len(updates) > 1000 {
			return protocolError()
		}
		for _, v := range updates {
			o := asObject(v)
			if guid := o.str("object_guid"); !guidPattern.MatchString(guid) {
				events = append(events, unsupportedPeerEvent(account, guid, "message", string(raw)))
				continue
			}
			event, e := c.projectMessage(o.str("object_guid"), o)
			if e != nil {
				return e
			}
			events = append(events, event)
		}
	}
	for _, chat := range asObjects(frame["chat_updates"]) {
		guid := chat.str("object_guid")
		if !guidPattern.MatchString(guid) {
			events = append(events, unsupportedPeerEvent(account, guid, "chat", chat.str("timestamp")))
			continue
		}
		payload, _ := json.Marshal(publicObject(asObject(chat["chat"])))
		events = append(events, domains.Event{Provider: domains.ProviderRubika, ID: eventHash("chat_stream|" + guid + "|" + chat.str("timestamp") + "|" + string(payload)), Type: "chat.updated", Peer: peerFromGUID(guid), Direction: "unknown", Time: time.Now().UTC(), Payload: payload})
	}
	if len(events) == 0 {
		data := json.RawMessage(`{"supported":false}`)
		events = append(events, domains.Event{Provider: domains.ProviderRubika, ID: eventHash("unsupported|" + string(raw)), Type: "protocol.unsupported_update", Direction: "unknown", Time: time.Now().UTC(), Payload: data})
	}
	if len(events) > 1000 {
		return protocolError()
	}
	if e := sink(ctx, domains.EventBatch{Events: events}); e != nil {
		return e
	}
	c.observeCoverage(events)
	return nil
}
func peerFromGUID(guid string) domains.Peer {
	kind := ""
	if strings.HasPrefix(guid, "u0") {
		kind = "user"
	}
	if strings.HasPrefix(guid, "b0") {
		kind = "bot"
	}
	if strings.HasPrefix(guid, "s0") {
		kind = "service"
	}
	if strings.HasPrefix(guid, "g0") {
		kind = "group"
	}
	if strings.HasPrefix(guid, "c0") {
		kind = "channel"
	}
	return domains.Peer{Type: kind, ID: guid}
}
func (c *Client) projectMessage(guid string, update object) (domains.Event, error) {
	if !guidPattern.MatchString(guid) || update == nil || (update.str("object_guid") != "" && update.str("object_guid") != guid) {
		return domains.Event{}, protocolError()
	}
	action := update.str("action")
	id := update.str("message_id")
	message := asObject(update["message"])
	if id == "" {
		id = message.str("message_id")
	}
	if !validMessageID(id) || (message.str("message_id") != "" && message.str("message_id") != id) {
		return domains.Event{}, protocolError()
	}
	kind := "message"
	switch action {
	case "New":
	case "Edit":
		kind = "message.edited"
	case "Delete":
		kind = "message.deleted"
	default:
		return domains.Event{}, protocolError()
	}
	identity := kind + "|" + guid + "|" + id
	if action != "New" {
		if update.str("timestamp") == "" {
			return domains.Event{}, protocolError()
		}
		identity += "|" + update.str("timestamp")
	}
	event := domains.Event{Provider: domains.ProviderRubika, ID: eventHash(identity), Type: kind, Peer: peerFromGUID(guid), MessageID: id, Direction: "unknown", Time: time.Now().UTC()}
	if action == "Delete" {
		payload, _ := json.Marshal(object{"provider_update_timestamp": update.str("timestamp"), "timestamp_source": "observed"})
		event.Payload = payload
		return event, nil
	}
	// Difference replies can contain a patch rather than a complete message.
	// Preserve the patch and its provider revision without inventing the original
	// author, direction, creation time or attachment. Rejecting it would stall all
	// pending conversations behind this peer on every restart.
	if action == "Edit" && message != nil && message["time"] == nil {
		patch := &domains.MessagePatch{ID: id, ChatID: guid, OriginalMessageID: id, Partial: true}
		payload := object{"partial": true, "timestamp_source": "observed", "provider_update_timestamp": update.str("timestamp"), "supported": false}
		if value, present := message["text"]; present {
			text, ok := value.(string)
			if !ok {
				return domains.Event{}, protocolError()
			}
			patch.Body, patch.Supported = &text, true
			payload["text"] = text
			payload["supported"] = true
		}
		if edited, ok := message["is_edited"].(bool); ok {
			payload["is_edited"] = edited
		}
		// Private/unreviewed metadata is deliberately not projected.
		payload["unprojected_fields"] = false
		for field := range message {
			if field != "text" && field != "is_edited" {
				payload["unprojected_fields"] = true
			}
		}
		event.Payload, _ = json.Marshal(payload)
		event.MessagePatch = patch
		return event, nil
	}
	if message == nil || message.num("time") <= 0 {
		return domains.Event{}, protocolError()
	}
	event.Time = time.Unix(message.num("time"), 0).UTC()
	event.SenderID = message.str("author_object_guid")
	c.mu.RLock()
	self := c.session.UserID
	c.mu.RUnlock()
	var fromMe *bool
	if (Contract{}).ValidateSenderID(event.SenderID) {
		value := event.SenderID == self
		fromMe = &value
		event.Direction = "incoming"
		if value {
			event.Direction = "outgoing"
		}
	}
	msg := &domains.Message{ID: id, ChatID: event.Peer.ID, From: event.SenderID, SenderNameStatus: "unavailable", IsFromMe: fromMe, Timestamp: event.Time, Body: message.str("text"), Kind: "text", Supported: message.str("type") == "Text", RepliedToID: message.str("reply_to_message_id")}
	if action == "Edit" {
		msg.OriginalMessageID = id
	}
	if message["file_inline"] != nil {
		ref, public, err := c.projectMedia(asObject(message["file_inline"]))
		if err != nil {
			return domains.Event{}, err
		}
		event.Media = ref
		msg.Media = public
		if public != nil {
			msg.Kind = public.Type
			msg.Supported = true
		} else {
			msg.Kind = "unsupported"
			msg.Supported = false
		}
	}
	event.Message = msg
	payload := object{"text": msg.Body, "kind": msg.Kind, "supported": msg.Supported, "provider_update_timestamp": update.str("timestamp")}
	if extra, handled, err := projectExtendedMessage(message, msg); err != nil {
		return domains.Event{}, err
	} else if handled {
		for key, value := range extra {
			payload[key] = value
		}
		payload["kind"] = msg.Kind
		payload["supported"] = msg.Supported
	}
	event.Payload, _ = json.Marshal(payload)
	return event, nil
}

var _ domains.Client = (*Client)(nil)
var _ domains.BatchClient = (*Client)(nil)
var _ domains.AuthChallengeSource = (*Client)(nil)

func pollJitter(base time.Duration) time.Duration {
	return min(60*time.Second, max(time.Millisecond, base*time.Duration(80+rand.IntN(41))/100))
}

// Coverage is a local connection observation; durable events remain its audit
// source. Only mark it after the complete event batch has been accepted.
func (c *Client) observeCoverage(events []domains.Event) {
	for _, event := range events {
		var patchMetadata struct {
			Unprojected bool `json:"unprojected_fields"`
		}
		if event.MessagePatch != nil {
			_ = json.Unmarshal(event.Payload, &patchMetadata)
		}
		if event.Type == "protocol.unsupported_update" || (event.Message != nil && !event.Message.Supported) || patchMetadata.Unprojected {
			c.mu.Lock()
			c.status.UnsupportedUpdatesObserved = true
			c.mu.Unlock()
			return
		}
	}
}
func (c *Client) setRecoveryStatus(cp checkpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Recovery = "degraded"
	c.status.RecoveryIssue = cp.GapReason
	if cp.Gap && cp.GapReason != "legacy_unclassified" {
		c.status.Recovery = "gap_detected"
	}
	if cp.Listing || cp.RefreshPeers || len(cp.Pending) > 0 {
		if !cp.Gap {
			c.status.Recovery = "recovering"
		}
	}
	c.status.Transport = "connected"
	c.status.LastError = ""
}
