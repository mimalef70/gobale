// Package balemeow implements a native Go client for an explicitly reviewed
// subset of the unofficial Bale protocol. Transport connectivity is not a
// guarantee of update recovery; Status.Recovery stays degraded until a verified
// recovery implementation is available. No automatic resend is performed.
package balemeow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

// Options describes endpoint/identity configuration and bounded resource use.
// AppID and APIKey must be supplied by the operator; there is no bundled key.
// Endpoint overrides are useful for isolated fake-server tests. API version and
// framing defaults reflect the inspected official web client. Live verification
// is documented separately from fake-server coverage.
type Options struct {
	GRPCEndpoint           string
	WebSocketEndpoint      string
	Origin                 string
	AppID                  uint32
	APIKey                 string
	APIVersion             uint32
	HandshakeAPIVersion    int64
	DeviceTitle            string
	HTTPClient             *http.Client
	LoadCheckpoint         func(context.Context) (string, error)
	SaveMediaReference     func(context.Context, domains.Peer, string, domains.ProviderMedia) (bool, error)
	RecoveryVerified       bool
	MediaSource            func(context.Context, string) (io.ReadCloser, MediaInfo, error)
	MaxMediaBytes          int64
	MediaTimeout           time.Duration
	MediaAllowedHosts      []string
	OnDiagnostic           func(Diagnostic)
	RequestTimeout         time.Duration
	HandshakeTimeout       time.Duration
	DrainTimeout           time.Duration
	PingInterval           time.Duration
	PingTimeout            time.Duration
	MaxFrameBytes          int64
	MaxPending             int
	EventBuffer            int
	MaxBufferedUpdateBytes int64
}

type authChallenge struct {
	id, transaction, phone, deviceHash string
	expires                            time.Time
}
type rpcReply struct {
	payload []byte
	err     error
}
type pendingRPC struct{ result chan rpcReply }
type connection struct {
	account             string
	token               string
	sink                domains.Sink
	checkpoint          recoveryCheckpoint
	recoveryFailed      bool
	bufferedUpdateBytes int64
	ws                  *websocket.Conn
	ctx                 context.Context
	cancel              context.CancelFunc
	handshake           chan error
	updates             chan []byte
	writeGate           chan struct{}
	workers             sync.WaitGroup
	waitOnce            sync.Once
	done                chan struct{}
	mu                  sync.Mutex
	pending             map[int64]*pendingRPC
	nextIndex           int64
	pingID              int64
	pingAt              time.Time
	once                sync.Once
}

type Client struct {
	opts                Options
	peerHashes          map[string]int64
	senderNames         map[string]senderName
	senderLookupAfter   time.Time
	senderContactsAfter time.Time
	mu                  sync.Mutex
	authMu              sync.Mutex
	session             *domains.Session
	sink                domains.Sink
	status              domains.ConnectionStatus
	challenge           *authChallenge
	conn                *connection
	lastConn            *connection
	connecting          chan struct{}
	connectErr          error
	connectCancel       context.CancelFunc
}

var _ domains.Client = (*Client)(nil)

func New(opts Options) *Client {
	if opts.GRPCEndpoint == "" {
		opts.GRPCEndpoint = "https://next-ws.bale.ai"
	}
	if opts.WebSocketEndpoint == "" {
		opts.WebSocketEndpoint = "wss://next-ws.bale.ai/ws/"
	}
	if opts.Origin == "" {
		opts.Origin = "https://web.bale.ai"
	}
	if opts.HandshakeAPIVersion <= 0 {
		opts.HandshakeAPIVersion = 1
	}
	if opts.APIVersion == 0 {
		opts.APIVersion = 173855
	}
	if opts.DeviceTitle == "" {
		opts.DeviceTitle = "GoBale"
	}
	if opts.MaxMediaBytes <= 0 {
		opts.MaxMediaBytes = 64 << 20
	}
	if opts.MediaTimeout <= 0 {
		opts.MediaTimeout = 2 * time.Minute
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 20 * time.Second
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = 8 * time.Second
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = 5 * time.Second
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = 10 * time.Second
	}
	if opts.PingTimeout <= 0 {
		opts.PingTimeout = 5 * time.Second
	}
	if opts.MaxFrameBytes <= 0 {
		opts.MaxFrameBytes = 4 << 20
	}
	if opts.MaxPending <= 0 {
		opts.MaxPending = 128
	}
	if opts.MaxBufferedUpdateBytes <= 0 {
		opts.MaxBufferedUpdateBytes = 16 << 20
	}
	if opts.EventBuffer <= 0 {
		opts.EventBuffer = 128
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: opts.RequestTimeout}
	}
	// Never forward authentication credentials on a redirect, including redirects
	// to a second provider endpoint. An ambiguous call is never retried elsewhere.
	copied := *hc
	// Every client gets an isolated jar; provider login cookies must never cross devices.
	copied.Jar = newAccountCookieJar()
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	opts.HTTPClient = &copied
	return &Client{opts: opts, peerHashes: make(map[string]int64), status: domains.ConnectionStatus{Auth: "unauthenticated", Transport: "disconnected", Recovery: "degraded"}}
}

func (c *Client) Status() domains.ConnectionStatus { c.mu.Lock(); defer c.mu.Unlock(); return c.status }

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func boundedError(code, msg string, status int) error { return domains.E(code, msg, status) }
func protocolError() error {
	return boundedError("PROTOCOL_ERROR", "provider returned an invalid or unsupported protocol response", 502)
}
func unavailable() error {
	return &domains.Error{Code: "CONNECTION_UNAVAILABLE", Message: "device is not connected", HTTP: 503, Retryable: true}
}
func ambiguous() error {
	return &domains.Error{Code: "SEND_UNKNOWN", Message: "request may have reached the provider; reconcile before retrying", HTTP: 202, Ambiguous: true}
}
func decode(b []byte, m proto.Message) error {
	if err := preflightDecode(b, m.ProtoReflect().Descriptor()); err != nil {
		return err
	}
	return (proto.UnmarshalOptions{RecursionLimit: 32, DiscardUnknown: false}).Unmarshal(b, m)
}

func (c *Client) metadata(token string) *wire.Metadata {
	version := strconv.FormatUint(uint64(c.opts.APIVersion), 10)
	sessionID := strconv.FormatInt(time.Now().UnixMilli(), 10)
	values := [][2]string{{"app_version", version}, {"browser_type", "1"}, {"browser_version", "154.0.0.0"}, {"os_type", "5"}, {"session_id", sessionID}, {"language", "fa"}, {"mt_app_version", version}, {"mt_browser_type", "1"}, {"mt_browser_version", "154.0.0.0"}, {"mt_os_type", "5"}, {"mt_session_id", sessionID}, {"mt_language", "fa"}}
	if token != "" {
		values = append(values, [2]string{"auth-jwt", token})
	}
	m := &wire.Metadata{}
	for _, kv := range values {
		m.Items = append(m.Items, &wire.MetadataItem{Key: kv[0], Value: &wire.MetadataValue{StringValue: kv[1]}})
	}
	return m
}

// Connect waits for a completed handshake. Concurrent callers share the attempt.
// Successful connections own their lifetime independently of the caller's context.
func (c *Client) Connect(ctx context.Context, session *domains.Session, sink domains.Sink) error {
	if session == nil || session.Token == "" || session.UserID == "" {
		return boundedError("AUTH_REQUIRED", "a complete authenticated session is required", 401)
	}
	if _, err := strconv.ParseUint(session.UserID, 10, 32); err != nil {
		return boundedError("INVALID_SESSION", "session user ID is invalid", 400)
	}
	c.mu.Lock()
	if c.status.Auth == "auth_required" {
		c.mu.Unlock()
		return boundedError("AUTH_REQUIRED", "session was revoked; authenticate again", 401)
	}
	if c.session != nil && c.session.UserID != session.UserID {
		c.mu.Unlock()
		return boundedError("ACCOUNT_CONFLICT", "use a new device for a different account", 409)
	}
	if c.connecting != nil {
		wait := c.connecting
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		}
		c.mu.Lock()
		err := c.connectErr
		c.mu.Unlock()
		return err
	}
	if c.conn != nil && c.status.Transport == "connected" {
		c.mu.Unlock()
		return nil
	}
	if sink == nil {
		c.mu.Unlock()
		return boundedError("INVALID_SINK", "a durable event sink is required", 400)
	}
	stored := *session
	stored.Data = append([]byte(nil), session.Data...)
	c.session = &stored
	c.sink = func(ctx context.Context, event domains.Event) error {
		return sink(ctx, c.prepareEvent(ctx, event))
	}
	c.status.Auth = "authenticated"
	c.status.Transport = "connecting"
	c.status.Recovery = "degraded"
	c.status.LastError = ""
	done := make(chan struct{})
	c.connecting = done
	connectCtx, cancel := context.WithTimeout(ctx, c.opts.HandshakeTimeout)
	c.connectCancel = cancel
	previous := c.lastConn
	c.mu.Unlock()
	err := waitConnection(connectCtx, previous)
	if err == nil {
		err = c.dial(connectCtx)
	}
	cancel()
	c.mu.Lock()
	c.connectErr = err
	c.connectCancel = nil
	c.connecting = nil
	if err != nil && c.status.Auth != "auth_required" {
		c.status.Transport = "disconnected"
		c.status.LastError = "CONNECT_FAILED"
	}
	close(done)
	c.mu.Unlock()
	return err
}

func (c *Client) dial(ctx context.Context) error {
	c.mu.Lock()
	if c.session == nil || c.status.Auth == "auth_required" {
		c.mu.Unlock()
		return boundedError("AUTH_REQUIRED", "session was revoked; authenticate again", 401)
	}
	session := *c.session
	sink := c.sink
	c.mu.Unlock()
	u, err := url.Parse(c.opts.WebSocketEndpoint)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
		return boundedError("INVALID_CONFIG", "invalid WebSocket endpoint", 500)
	}

	header := http.Header{}
	header.Set("Origin", c.opts.Origin)
	header.Set("auth-jwt", session.Token)
	if err := c.restoreCookies(session.Data); err != nil {
		return err
	}
	ws, response, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.opts.HTTPClient, HTTPHeader: header, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		message := "unable to open provider WebSocket"
		if response != nil {
			message += " (HTTP " + strconv.Itoa(response.StatusCode) + ")"
		}
		return boundedError("CONNECT_FAILED", message, 503)
	}
	ws.SetReadLimit(c.opts.MaxFrameBytes)
	lifetime, cancel := context.WithCancel(context.Background())
	conn := &connection{account: session.UserID, token: session.Token, writeGate: make(chan struct{}, 1), done: make(chan struct{}), sink: sink, ws: ws, ctx: lifetime, cancel: cancel, handshake: make(chan error, 1), updates: make(chan []byte, c.opts.EventBuffer), pending: make(map[int64]*pendingRPC)}
	conn.workers.Add(1)
	c.mu.Lock()
	if c.session == nil || c.session.Token != session.Token || c.status.Auth == "auth_required" || ctx.Err() != nil {
		c.mu.Unlock()
		conn.cancel()
		_ = conn.ws.CloseNow()
		conn.workers.Done()
		return boundedError("AUTH_REQUIRED", "connection attempt cancelled or session changed", 401)
	}
	c.conn = conn
	c.lastConn = conn
	c.status.Transport = "handshaking"
	c.mu.Unlock()
	go func() { defer conn.workers.Done(); c.readLoop(conn) }()
	frame := &wire.ClientMessage{Handshake: &wire.HandshakeRequest{ProtocolVersion: 1, ApiVersion: c.opts.HandshakeAPIVersion}}
	if err = c.write(ctx, conn, frame); err != nil {
		c.fail(conn, "HANDSHAKE_FAILED", false)
		return boundedError("HANDSHAKE_FAILED", "unable to send provider handshake", 503)
	}
	select {
	case err = <-conn.handshake:
	case <-ctx.Done():
		err = boundedError("HANDSHAKE_TIMEOUT", "provider handshake timed out", 504)
	case <-conn.ctx.Done():
		c.mu.Lock()
		cause := c.status.LastError
		revoked := c.status.Auth == "auth_required"
		c.mu.Unlock()
		if revoked {
			err = boundedError("AUTH_REQUIRED", "provider rejected the WebSocket session during handshake", 401)
		} else {
			if cause == "" {
				cause = "CONNECT_FAILED"
			}
			err = boundedError(cause, "provider closed during handshake", 503)
		}
	}
	if err != nil {
		c.fail(conn, "HANDSHAKE_FAILED", false)
		return err
	}
	c.mu.Lock()
	if c.conn != conn || conn.ctx.Err() != nil {
		c.mu.Unlock()
		return boundedError("CONNECT_FAILED", "provider connection closed", 503)
	}
	c.status.Transport = "connected"
	c.status.Recovery = "degraded"
	conn.workers.Add(2)
	c.mu.Unlock()
	go func() { defer conn.workers.Done(); c.consumeUpdates(conn) }()
	go func() { defer conn.workers.Done(); c.heartbeat(conn) }()
	return nil
}

// beforeWireError proves that Write was never attempted. RPC callers may
// retry only these failures according to the enclosed domain error.
type beforeWireError struct{ cause error }

func (e *beforeWireError) Error() string { return e.cause.Error() }
func (e *beforeWireError) Unwrap() error { return e.cause }
func (c *Client) write(ctx context.Context, conn *connection, msg *wire.ClientMessage) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return &beforeWireError{protocolError()}
	}
	if int64(len(b)) > c.opts.MaxFrameBytes {
		return &beforeWireError{boundedError("FRAME_TOO_LARGE", "request exceeds provider frame limit", 413)}
	}
	select {
	case conn.writeGate <- struct{}{}:
		defer func() { <-conn.writeGate }()
	case <-ctx.Done():
		return &beforeWireError{unavailable()}
	case <-conn.ctx.Done():
		return &beforeWireError{unavailable()}
	}
	if ctx.Err() != nil || conn.ctx.Err() != nil {
		return &beforeWireError{unavailable()}
	}
	return conn.ws.Write(ctx, websocket.MessageBinary, b)
}
func waitConnection(ctx context.Context, conn *connection) error {
	if conn == nil {
		return nil
	}
	conn.waitOnce.Do(func() { go func() { conn.workers.Wait(); close(conn.done) }() })
	select {
	case <-conn.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) fail(conn *connection, code string, revoked bool) {
	// Revocation is an authentication-generation transition, independent of
	// which concurrent transport failure won the one-time cleanup race.
	if revoked {
		c.mu.Lock()
		var active *connection
		if c.session != nil && c.session.UserID == conn.account && c.session.Token == conn.token {
			c.session = nil
			c.status.Auth = "auth_required"
			c.status.LastError = "SESSION_REVOKED"
			active = c.conn
		}
		c.mu.Unlock()
		if active != nil && active != conn {
			c.fail(active, "SESSION_REVOKED", false)
		}
	}
	conn.once.Do(func() {
		conn.cancel()
		_ = conn.ws.CloseNow()
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
			c.status.Transport = "disconnected"
			if c.status.Recovery != "gap_detected" {
				c.status.Recovery = "degraded"
			}
			c.status.LastError = code

		}
		c.mu.Unlock()
		conn.mu.Lock()
		for index, p := range conn.pending {
			p.result <- rpcReply{err: ambiguous()}
			delete(conn.pending, index)
		}
		conn.mu.Unlock()
		select {
		case conn.handshake <- boundedError(code, "provider connection closed", 503):
		default:
		}
	})
}

func (c *Client) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	cancel := c.connectCancel
	pending := c.connecting
	conn := c.conn
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		c.fail(conn, "", false)
	}
	if pending != nil {
		select {
		case <-pending:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.mu.Lock()
	conn = c.conn
	last := c.lastConn
	c.mu.Unlock()
	if conn != nil {
		c.fail(conn, "", false)
	}
	return waitConnection(ctx, last)
}

func (c *Client) Logout(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.mu.Lock()
	hasSession := c.session != nil
	c.mu.Unlock()
	if !hasSession {
		return boundedError("AUTH_REQUIRED", "no authenticated session to revoke", 401)
	}
	if _, err := c.unary(ctx, "SignOut", &wire.Empty{}); err != nil {
		return err
	}
	if err := c.Disconnect(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.session = nil
	c.challenge = nil
	c.status.Auth = "unauthenticated"
	c.peerHashes = make(map[string]int64)
	c.senderNames = nil
	c.senderLookupAfter = time.Time{}
	c.senderContactsAfter = time.Time{}
	c.clearCookies()
	c.mu.Unlock()
	return nil
}

func (c *Client) Send(ctx context.Context, request domains.SendRequest) (domains.SendResult, error) {
	if err := request.Validate(); err != nil {
		return domains.SendResult{}, err
	}

	if request.Phone != "" {
		resolved, err := c.ResolvePhone(ctx, request.Phone)
		if err != nil {
			return domains.SendResult{}, err
		}
		request.Peer = resolved
	}

	peer, err := c.messagePeer(ctx, request.Peer)
	if err != nil {
		return domains.SendResult{}, err
	}
	rid, err := positiveID(request.RequestID)
	if err != nil {
		return domains.SendResult{}, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before sending", 400)
	}
	if request.Kind != "" && request.Kind != "text" {
		return c.sendMedia(ctx, request, peer, rid)
	}
	payload := &wire.SendMessageRequest{Peer: peer, ExPeer: extendedPeer(peer, request.Peer), Rid: rid, Message: &wire.Message{Text: mentionedText(request.Text, request.Mentions)}}
	if request.ReplyMessageID != "" {
		quoted, err := messageID(request.ReplyMessageID)
		if err != nil {
			return domains.SendResult{}, boundedError("INVALID_REQUEST", "reply_message_id must be a nonzero signed int64", 400)
		}
		payload.QuotedMessage = &wire.MessageReference{Peer: peer, Rid: quoted}
	}
	data, err := c.rpc(ctx, "bale.messaging.v2.Messaging", "SendMessage", payload)
	if err != nil {
		return domains.SendResult{}, err
	}
	reply := &wire.SendMessageResponse{}
	if decode(data, reply) != nil || reply.Date <= 0 {
		return domains.SendResult{}, ambiguous()
	}
	return domains.SendResult{MessageID: strconv.FormatInt(rid, 10), Date: time.UnixMilli(reply.Date).UTC()}, nil
}

func positiveID(s string) (int64, error) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("invalid ID")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid ID")
	}
	return n, nil
}

// Provider message IDs are signed int64 (other clients may choose negative RIDs).
// New outgoing request IDs are generated separately and remain positive.
func messageID(s string) (int64, error) {
	digits := strings.TrimPrefix(s, "-")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, errors.New("invalid message ID")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New("invalid message ID")
	}
	return n, nil
}

func encodePeer(peer domains.Peer) (*wire.Peer, error) {
	if err := peer.Validate(); err != nil {
		return nil, err
	}
	id, err := strconv.ParseUint(peer.ID, 10, 32)
	if err != nil || id == 0 {
		return nil, boundedError("INVALID_PEER", "peer.id must be a positive uint32 decimal string", 400)
	}
	kind := int32(1)
	if peer.Type == "group" || peer.Type == "channel" {
		kind = 2
	}
	hash := int64(0)
	if peer.AccessHash != "" {
		hash, err = strconv.ParseInt(peer.AccessHash, 10, 64)
		if err != nil {
			return nil, boundedError("INVALID_PEER", "access_hash must be a signed int64 decimal string", 400)
		}
	}
	return &wire.Peer{Type: kind, Id: uint32(id), AccessHash: hash}, nil
}

// Call exposes only reviewed operation shapes. No arbitrary provider RPC proxy
// is provided: broken schemas cannot silently turn into successful operations.
func (c *Client) Call(ctx context.Context, operation string, raw json.RawMessage) (json.RawMessage, error) {
	if _, ok := domains.OperationDefinition(operation); ok {
		var err error
		raw, err = prepareExtendedCall(operation, raw)
		if err != nil {
			return nil, err
		}
	}
	switch operation {
	case "contacts.resolve":
		var p struct {
			Phone string `json:"phone"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return nil, boundedError("INVALID_REQUEST", "phone is required", 400)
		}
		peer, err := c.ResolvePhone(ctx, p.Phone)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"peer": peer})
	case "send.poll", "poll.vote", "poll.close", "poll.results", "poll.full_results":
		return c.pollExtended(ctx, operation, raw)
	case "sticker.list", "sticker.get", "sticker.pack.add", "sticker.pack.remove", "send.sticker":
		return c.stickerExtended(ctx, operation, raw)
	case "report.peer", "report.messages", "report.story", "report.dismiss", "message.upvoters", "message.upvote", "message.upvote.remove":
		return c.accountBusinessCall(ctx, operation, raw)
	case "wallet.list", "wallet.kifpools":
		return c.accountWalletCall(ctx, operation, raw)
	case "miniapp.url", "miniapp.hash", "miniapp.menu", "miniapp.data", "miniapp.custom", "bot.callback", "link.summary":
		return c.miniAppExtended(ctx, operation, raw)
	case "story.list", "story.get", "story.viewers", "story.add", "story.delete", "story.react":
		return c.storyExtended(ctx, operation, raw)
	case "send.contact", "send.location", "send.template":
		return c.richMessageCall(ctx, operation, raw)
	case "account.info":
		return c.accountExtendedCall(ctx, operation, raw)
	case "chat.clear", "chat.delete", "message.received", "message.pin", "message.unpin", "message.unpin_all", "message.pins", "folders.list", "folders.create", "folders.edit", "folders.delete":
		return c.messagingExtended(ctx, operation, raw)
	case "group.description", "group.remove":
		return c.groupMutation(ctx, operation, raw)
	case "message.forward", "message.delete":
		return c.messageMutation(ctx, operation, raw)
	case "contacts.list", "contacts.search":
		return c.contacts(ctx, operation, raw)
	case "group.info", "group.link":
		return c.infoCall(ctx, operation, raw)
	case "group.list", "channel.list", "group.members", "group.create", "channel.create", "group.invite", "group.title":
		return c.groupCall(ctx, operation, raw)
	case "chat.history", "chat.messages":
		return c.history(ctx, raw)
	case "chat.list", "chats":
		return c.dialogs(ctx, raw)
	case "message.read":
		var p struct {
			Peer domains.Peer `json:"peer"`
			Date string       `json:"date"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return nil, boundedError("INVALID_REQUEST", "invalid message.read body", 400)
		}
		peer, err := c.messagePeer(ctx, p.Peer)
		if err != nil {
			return nil, err
		}
		date, err := positiveID(p.Date)
		if err != nil {
			return nil, boundedError("INVALID_REQUEST", "date must be a positive decimal millisecond timestamp", 400)
		}
		_, err = c.rpc(ctx, "bale.messaging.v2.Messaging", "MessageRead", &wire.MessageReadRequest{Peer: peer, Date: date})
		if err != nil {
			return nil, err
		}
		return json.RawMessage(`{"acknowledged":true}`), nil
	case "message.edit":
		var p struct {
			Peer      domains.Peer `json:"peer"`
			MessageID string       `json:"message_id"`
			Text      string       `json:"message"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return nil, boundedError("INVALID_REQUEST", "invalid message.edit body", 400)
		}
		peer, err := c.messagePeer(ctx, p.Peer)
		if err != nil {
			return nil, err
		}
		rid, err := messageID(p.MessageID)
		if err != nil || p.Text == "" || len(p.Text) > 65536 {
			return nil, boundedError("INVALID_REQUEST", "nonzero signed message_id and message up to 64 KiB are required", 400)
		}
		_, err = c.rpc(ctx, "bale.messaging.v2.Messaging", "UpdateMessage", &wire.UpdateMessageRequest{Peer: peer, Rid: rid, Message: &wire.Message{Text: &wire.TextMessage{Text: p.Text}}})
		if err != nil {
			return nil, err
		}
		return json.RawMessage(`{"acknowledged":true}`), nil
	default:
		if _, known := domains.OperationDefinition(operation); known {
			switch {
			case strings.HasPrefix(operation, "group."):
				return c.groupExtended(ctx, operation, raw)
			case strings.HasPrefix(operation, "account."), strings.HasPrefix(operation, "contacts."), operation == "users.get":
				return c.accountExtendedCall(ctx, operation, raw)
			case strings.HasPrefix(operation, "presence."), strings.HasPrefix(operation, "message.reaction"), strings.HasPrefix(operation, "message.views"):
				return c.presenceReactionCall(ctx, operation, raw)
			}
		}
		return nil, domains.Unsupported(operation)
	}
}
