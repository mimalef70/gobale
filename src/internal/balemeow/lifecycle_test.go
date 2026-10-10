package balemeow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
)

func TestWriterGateHonorsCancellationBeforeWire(t *testing.T) {
	var requests atomic.Int32
	server := newFakeWS(t, func(_ *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			requests.Add(1)
		}
	})
	c := server.client()
	connectTest(t, c, acceptingSink)
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	conn.writeGate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, e := c.Send(ctx, domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Text: "hello", RequestID: "123"})
	<-conn.writeGate
	require.Less(t, time.Since(started), 300*time.Millisecond)
	var de *domains.Error
	require.True(t, errors.As(e, &de))
	require.Equal(t, "CONNECTION_UNAVAILABLE", de.Code)
	require.True(t, de.Retryable)
	require.False(t, de.Ambiguous)
	require.Equal(t, int32(0), requests.Load())
	require.Equal(t, "connected", c.Status().Transport)
}

func TestPreWireFrameLimitDoesNotInvalidateConnection(t *testing.T) {
	server := newFakeWS(t, nil)
	c := New(Options{WebSocketEndpoint: "ws" + strings.TrimPrefix(server.server.URL, "http"), MaxFrameBytes: 256, PingInterval: time.Hour})
	connectTest(t, c, acceptingSink)
	_, e := c.Send(context.Background(), domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Text: strings.Repeat("x", 1024), RequestID: "123"})
	require.Equal(t, "FRAME_TOO_LARGE", codeOf(e))
	require.Equal(t, "connected", c.Status().Transport)
}

func TestDisconnectHasOneDrainDeadlineAndJoinsConsumer(t *testing.T) {
	server := newFakeWS(t, nil)
	c := server.client()
	c.opts.RequestTimeout = time.Second
	c.opts.DrainTimeout = 25 * time.Millisecond
	entered := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	connectTest(t, c, func(ctx context.Context, _ domains.Event) error {
		calls.Add(1)
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return ctx.Err()
	})
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	for i := int64(1); i <= 10; i++ {
		b := marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 1, Id: 42}, SenderId: 42, Date: 1720000000000, Rid: i, Message: &wire.Message{Text: &wire.TextMessage{Text: "hello"}}}})
		server.connections.Range(func(k, v any) bool {
			server.send(k.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Update: b})}})
			return true
		})
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sink not entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	require.NoError(t, c.Disconnect(ctx))
	require.Less(t, time.Since(started), 300*time.Millisecond)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "EVENT_DRAIN_INCOMPLETE", c.Status().LastError)
	require.Equal(t, "gap_detected", c.Status().Recovery)
	select {
	case <-conn.done:
	default:
		t.Fatal("disconnect returned before connection workers stopped")
	}
}

func TestRevocationAfterTransportCleanupStillInvalidatesSession(t *testing.T) {
	server := newFakeWS(t, nil)
	c := server.client()
	connectTest(t, c, acceptingSink)
	c.mu.Lock()
	old := c.conn
	c.mu.Unlock()
	c.fail(old, "CONNECTION_LOST", false)
	c.fail(old, "SESSION_REVOKED", true)
	require.Equal(t, "auth_required", c.Status().Auth)
	require.Equal(t, "AUTH_REQUIRED", codeOf(c.Connect(context.Background(), fakeSession(), acceptingSink)))
}

func TestStaleRevocationCannotInvalidateNewAuthentication(t *testing.T) {
	server := newFakeWS(t, nil)
	c := server.client()
	connectTest(t, c, acceptingSink)
	c.mu.Lock()
	old := c.conn
	c.mu.Unlock()
	c.fail(old, "CONNECTION_LOST", false)
	session := fakeSession()
	session.Token = "new-authentication-token"
	require.NoError(t, c.Connect(context.Background(), session, acceptingSink))
	c.fail(old, "SESSION_REVOKED", true)
	require.Equal(t, "authenticated", c.Status().Auth)
	require.Equal(t, "connected", c.Status().Transport)
}

func TestRevocationInvalidatesReconnectedSameAuthentication(t *testing.T) {
	server := newFakeWS(t, nil)
	c := server.client()
	connectTest(t, c, acceptingSink)
	c.mu.Lock()
	old := c.conn
	c.mu.Unlock()
	c.fail(old, "CONNECTION_LOST", false)
	require.NoError(t, c.Connect(context.Background(), fakeSession(), acceptingSink))
	c.fail(old, "SESSION_REVOKED", true)
	require.Equal(t, "auth_required", c.Status().Auth)
	require.Equal(t, "disconnected", c.Status().Transport)
}

func TestUpdateExpansionAndDocumentValidation(t *testing.T) {
	ids := make([]int64, 4097)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	_, e := decodeEvents("123", marshal(t, &wire.UpdateContainer{Deleted: &wire.UpdateMessageDeleted{Peer: &wire.Peer{Type: 1, Id: 42}, Rids: ids}}))
	require.Equal(t, "PROTOCOL_ERROR", codeOf(e))
	for _, doc := range []*wire.DocumentMessage{{FileId: 0, FileSize: 10}, {FileId: 123, FileSize: -1}} {
		_, e := decodeEvents("123", marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 1, Id: 42}, SenderId: 42, Date: 1720000000000, Rid: 1, Message: &wire.Message{Document: doc}}}))
		require.Equal(t, "PROTOCOL_ERROR", codeOf(e))
	}
}
