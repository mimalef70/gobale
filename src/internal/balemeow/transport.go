package balemeow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func (c *Client) rpc(ctx context.Context, service, method string, payload proto.Message) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, unavailable()
	}
	encoded, err := proto.Marshal(payload)
	if err != nil {
		return nil, protocolError()
	}
	c.mu.Lock()
	conn := c.conn
	state := c.status.Transport
	token := ""
	if c.session != nil {
		token = c.session.Token
	}
	c.mu.Unlock()
	if conn == nil || state != "connected" {
		return nil, unavailable()
	}
	conn.mu.Lock()
	if len(conn.pending) >= c.opts.MaxPending {
		conn.mu.Unlock()
		return nil, boundedError("PROVIDER_BACKPRESSURE", "too many pending provider requests", 429)
	}
	conn.nextIndex++
	index := conn.nextIndex
	pending := &pendingRPC{result: make(chan rpcReply, 1)}
	conn.pending[index] = pending
	conn.mu.Unlock()
	defer func() { conn.mu.Lock(); delete(conn.pending, index); conn.mu.Unlock() }()
	request := &wire.ClientMessage{Request: &wire.Request{Service: service, Method: method, Payload: encoded, Metadata: c.metadata(token), Index: index}}
	callCtx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	// Once Write is attempted, the outcome is conservatively ambiguous. Neither
	// Write errors nor timeouts prove that the peer did not receive the bytes.
	if err = c.write(callCtx, conn, request); err != nil {
		var before *beforeWireError
		if errors.As(err, &before) {
			return nil, before.cause
		}
		c.fail(conn, "WRITE_FAILED", false)
		return nil, ambiguous()
	}
	select {
	case reply := <-pending.result:
		return reply.payload, reply.err
	case <-callCtx.Done():
		select {
		case reply := <-pending.result:
			return reply.payload, reply.err
		default:
			return nil, ambiguous()
		}
	case <-conn.ctx.Done():
		select {
		case reply := <-pending.result:
			return reply.payload, reply.err
		default:
			return nil, ambiguous()
		}
	}
}

func (c *Client) readLoop(conn *connection) {
	defer close(conn.updates)
	for {
		typ, data, err := conn.ws.Read(conn.ctx)
		if err != nil {
			status := websocket.CloseStatus(err)
			cause := "CONNECTION_LOST"
			if status > 0 {
				cause = "CONNECTION_CLOSED_" + strconv.Itoa(int(status))
			}
			c.fail(conn, cause, status == 4401)
			return
		}
		if typ != websocket.MessageBinary {
			c.fail(conn, "PROTOCOL_ERROR", false)
			return
		}
		packet := &wire.ServerMessage{}
		if decode(data, packet) != nil {
			c.fail(conn, "PROTOCOL_ERROR", false)
			return
		}
		if packet.TerminateSession != nil {
			c.fail(conn, "SESSION_REVOKED", true)
			return
		}
		if h := packet.Handshake; h != nil {
			var err error
			if h.ProtocolVersion != 1 || h.ApiVersion != c.opts.HandshakeAPIVersion {
				err = boundedError("PROTOCOL_VERSION_UNSUPPORTED", "provider handshake protocol version is unsupported", 502)
			}
			select {
			case conn.handshake <- err:
			default:
			}
		}
		if p := packet.Pong; p != nil {
			conn.mu.Lock()
			if p.Id == conn.pingID {
				conn.pingAt = time.Time{}
			}
			conn.mu.Unlock()
		}
		if r := packet.Response; r != nil {
			reply := rpcReply{payload: r.Payload}
			if r.Error != nil && r.Error.Code != 0 {
				reply.err = providerError(r.Error.Code, r.Error.Message)
			}
			conn.mu.Lock()
			if pending := conn.pending[r.Index]; pending != nil {
				delete(conn.pending, r.Index)
				pending.result <- reply
			}
			conn.mu.Unlock()
			if r.Error != nil && r.Error.Code == 16 {
				c.fail(conn, "SESSION_REVOKED", true)
				return
			}
		}
		if u := packet.Update; u != nil {
			conn.mu.Lock()
			wouldExceed := conn.bufferedUpdateBytes+int64(len(u.Payload)) > c.opts.MaxBufferedUpdateBytes
			if !wouldExceed {
				conn.bufferedUpdateBytes += int64(len(u.Payload))
			}
			conn.mu.Unlock()
			if wouldExceed {
				c.fail(conn, "EVENT_BACKPRESSURE", false)
				return
			}
			select {
			case conn.updates <- u.Payload:
			default:
				c.fail(conn, "EVENT_BACKPRESSURE", false)
				return
			}
		}
	}
}

func (c *Client) heartbeat(conn *connection) {
	period := c.opts.PingInterval
	if c.opts.PingTimeout < period {
		period = c.opts.PingTimeout
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	lastPing := time.Now()
	for {
		select {
		case <-conn.ctx.Done():
			return
		case now := <-ticker.C:
			conn.mu.Lock()
			outstanding := !conn.pingAt.IsZero()
			expired := outstanding && now.Sub(conn.pingAt) >= c.opts.PingTimeout
			due := !outstanding && now.Sub(lastPing) >= c.opts.PingInterval
			id := conn.pingID
			if due {
				conn.pingID++
				id = conn.pingID
				conn.pingAt = now
				lastPing = now
			}
			conn.mu.Unlock()
			if expired {
				c.fail(conn, "HEARTBEAT_TIMEOUT", false)
				return
			}
			if due {
				ctx, cancel := context.WithTimeout(conn.ctx, c.opts.PingTimeout)
				err := c.write(ctx, conn, &wire.ClientMessage{Ping: &wire.Ping{Id: id}})
				cancel()
				if err != nil {
					c.fail(conn, "HEARTBEAT_FAILED", false)
					return
				}
			}
		}
	}
}

// Provider errors intentionally omit the provider's free-form text. It may
// contain phone numbers, authentication material or other private input.
func providerError(code int32, message string) error {
	if requiresPassword(message) {
		return boundedError("PASSWORD_REQUIRED", "the account requires a password", 401)
	}
	switch code {
	case 3:
		return boundedError("PROVIDER_INVALID_ARGUMENT", "provider rejected the request arguments", 400)
	case 5:
		return boundedError("PROVIDER_NOT_FOUND", "provider did not find the requested resource", 404)
	case 7:
		return boundedError("PROVIDER_PERMISSION_DENIED", "provider denied this operation", 403)
	case 8:
		return boundedError("PROVIDER_RATE_LIMITED", "provider rate limit reached", 429)
	case 16:
		return boundedError("AUTH_REQUIRED", "provider requires authentication", 401)
	default:
		return boundedError("PROVIDER_REJECTED", fmt.Sprintf("provider rejected the request (status %d)", code), 502)
	}
}

// A timed-out read may safely be repeated; it cannot create an unknown send.
func (c *Client) readRPC(ctx context.Context, service, method string, payload proto.Message) ([]byte, error) {
	data, err := c.rpc(ctx, service, method, payload)
	var de *domains.Error
	if errors.As(err, &de) && de.Ambiguous {
		return nil, &domains.Error{Code: "PROVIDER_READ_FAILED", Message: "provider read did not complete", HTTP: 502, Retryable: true}
	}
	return data, err
}
