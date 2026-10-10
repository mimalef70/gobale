package balemeow

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
)

// A cold-name page legitimately takes longer than one RPC/storage timeout:
// the per-account rate limit waits between distinct senders. All its events
// must commit before the checkpoint without borrowing each other's budgets.
func TestRecoverySenderLookupsKeepIndependentCommitBudgets(t *testing.T) {
	const initial = `{"version":1,"account":"12345","routes":{"0":5}}`
	var diffCalls atomic.Int32
	var profileCalls atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		if recoveryInventoryFixture(t, fake, ws, m.Request) {
			return
		}
		r := m.Request
		switch r.Method {
		case "GetDiff":
			diff := &wire.RouteDiff{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: 5}}
			response := &wire.DiffResponse{Routes: []*wire.RouteDiff{diff}}
			if diffCalls.Add(1) > 1 {
				diff.State.Sequence = 8
				for n := uint32(0); n < 3; n++ {
					id := uint32(42) + n
					diff.Updates = append(diff.Updates, marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{
						Peer: &wire.Peer{Type: 1, Id: id}, SenderId: id, Rid: int64(6 + n), Date: 1720000000000 + int64(n),
						Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic recovery message"}},
					}}))
					response.Users = append(response.Users, &wire.PeerRef{Id: id, AccessHash: int64(9000 + n)})
				}
			}
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
		case "GetFullUser":
			q := &wire.GetUserInfoRequest{}
			require.NoError(t, decode(r.Payload, q))
			require.Equal(t, int64(9000+q.Peer.Id-42), q.Peer.AccessHash)
			profileCalls.Add(1)
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.GetUserInfoResponse{
				User: &wire.FullUser{Id: q.Peer.Id, Name: "Sender " + strconv.FormatUint(uint64(q.Peer.Id), 10)},
			})}})
		default:
			t.Errorf("unexpected RPC %s", r.Method)
		}
	})
	c := fake.client()
	c.senderLookupAfter = time.Time{}
	c.opts.RecoveryVerified = true
	c.opts.LoadCheckpoint = func(context.Context) (string, error) { return initial, nil }
	accepted := make(chan domains.Event, 5)
	connectTest(t, c, func(ctx context.Context, e domains.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "every durable acceptance must remain bounded")
		require.Greater(t, time.Until(deadline), c.opts.RequestTimeout/2, "enrichment must not consume the storage budget")
		accepted <- e
		return nil
	})
	select {
	case baseline := <-accepted:
		require.NotEmpty(t, baseline.Checkpoint)
	case <-time.After(time.Second):
		t.Fatal("initial checkpoint was not accepted")
	}
	start := time.Now()
	fake.connections.Range(func(k, _ any) bool {
		fake.send(k.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Sequence: 8, Update: syntheticUpdate(t, 8)})}})
		return true
	})
	for n := 0; n < 3; n++ {
		select {
		case event := <-accepted:
			require.Equal(t, strconv.Itoa(6+n), event.MessageID)
			require.Empty(t, event.Checkpoint, "checkpoint must follow every accepted page message")
			require.Equal(t, "Sender "+strconv.Itoa(42+n), event.Message.SenderDisplayName)
			require.Equal(t, "available", event.Message.SenderNameStatus)
		case <-time.After(2 * time.Second):
			t.Fatal("recovery stopped before all sender lookups committed")
		}
	}
	select {
	case marker := <-accepted:
		var cp recoveryCheckpoint
		require.NoError(t, json.Unmarshal([]byte(marker.Checkpoint), &cp))
		require.Equal(t, int32(8), cp.Routes["0"])
	case <-time.After(time.Second):
		t.Fatal("completed recovery page did not commit its checkpoint")
	}
	require.Greater(t, time.Since(start), c.opts.RequestTimeout)
	require.Equal(t, int32(3), profileCalls.Load())
	eventually(t, func() bool { return c.Status().Recovery == "current" })
}

func TestRecoverySinkTimeoutDoesNotAdvanceCheckpoint(t *testing.T) {
	const initial = `{"version":1,"account":"12345","routes":{"0":5}}`
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		if recoveryInventoryFixture(t, fake, ws, m.Request) {
			return
		}
		require.Equal(t, "GetDiff", m.Request.Method)
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.DiffResponse{
			Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: 5}}},
		})}})
	})
	c := fake.client()
	c.rememberUserName(&wire.User{Id: 42, Name: "Cached sender"})
	c.opts.LoadCheckpoint = func(context.Context) (string, error) { return initial, nil }
	var mu sync.Mutex
	checkpoint := initial
	baseline := make(chan struct{}, 1)
	failed := make(chan error, 1)
	connectTest(t, c, func(ctx context.Context, event domains.Event) error {
		if event.Type == "message" {
			<-ctx.Done()
			failed <- ctx.Err()
			return ctx.Err()
		}
		if event.Checkpoint != "" {
			mu.Lock()
			checkpoint = event.Checkpoint
			mu.Unlock()
			baseline <- struct{}{}
		}
		return nil
	})
	select {
	case <-baseline:
	case <-time.After(time.Second):
		t.Fatal("initial checkpoint was not accepted")
	}
	start := time.Now()
	fake.connections.Range(func(k, _ any) bool {
		fake.send(k.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Sequence: 6, Update: syntheticUpdate(t, 6)})}})
		return true
	})
	select {
	case err := <-failed:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("unresponsive sink exceeded its bounded deadline")
	}
	require.Less(t, time.Since(start), time.Second)
	eventually(t, func() bool { return c.Status().Recovery == "gap_detected" })
	mu.Lock()
	defer mu.Unlock()
	var cp recoveryCheckpoint
	require.NoError(t, json.Unmarshal([]byte(checkpoint), &cp))
	require.Equal(t, int32(5), cp.Routes["0"], "failed acceptance must not advance durable recovery")
}
