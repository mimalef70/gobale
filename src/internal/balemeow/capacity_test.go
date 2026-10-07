package balemeow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

// This measures real client transports against a local protocol fixture. It
// deliberately makes no claim about provider limits or live account capacity.
func TestOptionalNativeCapacity(t *testing.T) {
	if os.Getenv("GOBALE_NATIVE_CAPACITY") == "" {
		t.Skip("set GOBALE_NATIVE_CAPACITY=1 for isolated transport capacity")
	}
	accounts := 300
	if raw := os.Getenv("GOBALE_SOAK_ACCOUNTS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			t.Fatal("GOBALE_SOAK_ACCOUNTS must be 1..1000")
		}
		accounts = n
	}
	var rpc, updates, recovered atomic.Int64
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Ping != nil {
			fake.send(ws, &wire.ServerMessage{Pong: m.Ping})
			return
		}
		if m.Request == nil {
			return
		}
		rpc.Add(1)
		r := m.Request
		switch r.Method {
		case "GetRoutesStates":
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.RoutesResponse{States: []*wire.RouteState{{Group: &wire.PeerRef{}, Sequence: 1}}})}})
		case "GetDiff":
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.DiffResponse{Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: 1}}}})}})
		case "MessageRead":
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.Empty{})}})
		default:
			t.Errorf("unexpected fixture RPC %s", r.Method)
		}
	})
	fake.handshakeDelay = 100 * time.Millisecond
	clients := make([]*Client, accounts)
	for i := range clients {
		clients[i] = New(Options{WebSocketEndpoint: "ws" + strings.TrimPrefix(fake.server.URL, "http"), RequestTimeout: 5 * time.Second, HandshakeTimeout: 5 * time.Second, PingInterval: time.Hour})
		clients[i].opts.LoadCheckpoint = func(context.Context) (string, error) {
			return fmt.Sprintf(`{"version":1,"account":"%d","routes":{"0":1}}`, 100000+i), nil
		}
	}
	t.Cleanup(func() {
		for _, c := range clients {
			_ = c.Disconnect(context.Background())
		}
	})
	connect := func() time.Duration {
		started := time.Now()
		expectedRecovery := recovered.Load() + int64(accounts)
		jobs := make(chan int)
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for i := range jobs {
					session := fakeSession()
					session.UserID = strconv.Itoa(100000 + i)
					err := clients[i].Connect(context.Background(), session, func(_ context.Context, e domains.Event) error {
						if e.Type == "connection.recovery" && e.Checkpoint != "" {
							recovered.Add(1)
						}
						if e.Type == "message" {
							if e.AccountID != session.UserID {
								return fmt.Errorf("synthetic account isolation mismatch")
							}
							updates.Add(1)
						}
						return nil
					})
					if err != nil {
						t.Errorf("connect fixture account %d: %v", i, err)
					}
				}
			})
		}
		for i := range clients {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		deadline := started.Add(2 * time.Minute)
		for recovered.Load() < expectedRecovery && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if recovered.Load() != expectedRecovery {
			t.Fatalf("recovery checkpoints=%d expected=%d", recovered.Load(), expectedRecovery)
		}
		return time.Since(started)
	}
	first := connect()
	for _, c := range clients {
		_, err := c.Call(context.Background(), "message.read", json.RawMessage(`{"peer":{"type":"user","id":"77"},"date":"123"}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	fake.connections.Range(func(k, _ any) bool {
		fake.send(k.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Update: syntheticUpdate(t, 100)})}})
		return true
	})
	deadline := time.Now().Add(10 * time.Second)
	for updates.Load() < int64(accounts) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if updates.Load() != int64(accounts) {
		t.Fatalf("updates=%d accounts=%d", updates.Load(), accounts)
	}
	// Interrupt the sockets at the peer, then reconnect the same clients using
	// their restored synthetic sessions, with the production concurrency bound.
	fake.connections.Range(func(k, _ any) bool { _ = k.(*websocket.Conn).CloseNow(); return true })
	for _, c := range clients {
		_ = c.Disconnect(context.Background())
	}
	reconnected := connect()
	if reconnected > 2*time.Minute {
		t.Fatalf("local reconnect exceeded two minutes: %s", reconnected)
	}
	for _, c := range clients {
		if c.Status().Transport != "connected" {
			t.Fatal("client did not reconnect")
		}
	}
	report, _ := json.Marshal(map[string]any{"measurement_scope": "local protocol fixture, real balemeow clients; not live provider capacity", "accounts": accounts, "connect_workers": 4, "initial_connect_seconds": first.Seconds(), "reconnect_seconds": reconnected.Seconds(), "rpc_calls": rpc.Load(), "updates": updates.Load(), "passed": !t.Failed()})
	t.Log(string(report))
	if path := os.Getenv("GOBALE_SOAK_RESULT"); path != "" {
		if err := os.WriteFile(path, append(report, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
