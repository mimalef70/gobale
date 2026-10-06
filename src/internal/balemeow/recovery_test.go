package balemeow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"sync"
	"testing"
)

func syntheticUpdate(t *testing.T, rid int64) []byte {
	return marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 1, Id: 42}, SenderId: 42, Date: 1720000000000 + rid, Rid: rid, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}}})
}

func TestRecoveryBaselineAndRestartCatchup(t *testing.T) {
	var mu sync.Mutex
	checkpoint := ""
	events := []domains.Event{}
	load := func(context.Context) (string, error) { mu.Lock(); defer mu.Unlock(); return checkpoint, nil }
	sink := func(ctx context.Context, e domains.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
		if e.Checkpoint != "" {
			checkpoint = e.Checkpoint
		}
		return nil
	}
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		switch r.Method {
		case "GetRoutesStates":
			// A pre-baseline update already admitted must commit before its marker.
			fake.send(ws, &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Sequence: 5, Update: syntheticUpdate(t, 5)})}})
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.RoutesResponse{States: []*wire.RouteState{{Group: &wire.PeerRef{}, Sequence: 5}}})}})
		case "GetDiff":
			q := &wire.DiffRequest{}
			if decode(r.Payload, q) != nil || len(q.States) != 1 || q.States[0].Sequence != 5 {
				t.Error("wrong durable cursor")
			}
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.DiffResponse{Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: 7}, DatedUpdates: []*wire.DatedUpdate{{Update: syntheticUpdate(t, 6)}, {Update: syntheticUpdate(t, 7)}}}}})}})
		default:
			t.Errorf("unexpected RPC %s", r.Method)
		}
	})
	c := fake.client()
	c.opts.LoadCheckpoint = load
	connectTest(t, c, sink)
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return checkpoint != "" })
	mu.Lock()
	if len(events) < 2 || events[0].Type != "message" || events[1].Type != "connection.recovery" {
		t.Fatalf("wrong commit order: %v", events)
	}
	mu.Unlock()
	if err := c.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	c2 := fake.client()
	c2.opts.LoadCheckpoint = load
	connectTest(t, c2, sink)
	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		var cp recoveryCheckpoint
		_ = json.Unmarshal([]byte(checkpoint), &cp)
		return cp.Routes["0"] == 7
	})
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 5 || events[2].MessageID != "6" || events[3].MessageID != "7" || events[4].Checkpoint == "" {
		t.Fatalf("unexpected recovered events %+v", events)
	}
	if c2.Status().Recovery != "degraded" {
		t.Fatal("unverified recovery advertised current")
	}
}

func TestRecoveryDoesNotAdvanceBeforeSinkAndMarksTooLong(t *testing.T) {
	for _, tooLong := range []bool{false, true} {
		t.Run(map[bool]string{false: "sink_failure", true: "too_long"}[tooLong], func(t *testing.T) {
			cp := `{"version":1,"account":"12345","routes":{"0":5}}`
			var mu sync.Mutex
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				r := m.Request
				reply := &wire.DiffResponse{Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: 7}, Updates: [][]byte{syntheticUpdate(t, 6), syntheticUpdate(t, 7)}}}}
				if tooLong {
					reply.Routes[0].TooLong = &wire.BoolValue{Value: true}
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, reply)}})
			})
			c := fake.client()
			c.opts.LoadCheckpoint = func(context.Context) (string, error) { return cp, nil }
			connectTest(t, c, func(ctx context.Context, e domains.Event) error {
				if !tooLong && e.MessageID == "7" {
					return errors.New("synthetic disk full")
				}
				if e.Checkpoint != "" {
					mu.Lock()
					cp = e.Checkpoint
					mu.Unlock()
				}
				return nil
			})
			eventually(t, func() bool { return c.Status().Recovery == "gap_detected" })
			mu.Lock()
			defer mu.Unlock()
			var saved recoveryCheckpoint
			_ = json.Unmarshal([]byte(cp), &saved)
			if saved.Routes["0"] != 5 {
				t.Fatalf("checkpoint advanced after incomplete recovery: %s", cp)
			}
			if tooLong && !saved.Gap {
				t.Fatal("provider tooLong gap not persisted")
			}
		})
	}
}

func TestRecoveryStreamJumpReconcilesBeforeCurrent(t *testing.T) {
	var mu sync.Mutex
	checkpoint := `{"version":1,"account":"12345","routes":{"0":5}}`
	ids := []string{}
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		q := &wire.DiffRequest{}
		_ = decode(r.Payload, q)
		seq := q.States[0].Sequence
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.DiffResponse{Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: seq}}}})}})
	})
	c := fake.client()
	c.opts.RecoveryVerified = true
	c.opts.LoadCheckpoint = func(context.Context) (string, error) { mu.Lock(); defer mu.Unlock(); return checkpoint, nil }
	connectTest(t, c, func(ctx context.Context, e domains.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if e.Checkpoint != "" {
			checkpoint = e.Checkpoint
		}
		if e.Type == "message" {
			ids = append(ids, e.MessageID)
		}
		return nil
	})
	eventually(t, func() bool { return c.Status().Recovery == "current" })
	fake.connections.Range(func(k, v any) bool {
		fake.send(k.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Sequence: 8, Update: syntheticUpdate(t, 8)})}})
		return true
	})
	eventually(t, func() bool { return c.Status().Recovery == "gap_detected" })
	mu.Lock()
	defer mu.Unlock()
	var saved recoveryCheckpoint
	_ = json.Unmarshal([]byte(checkpoint), &saved)
	if saved.Routes["0"] != 5 || len(ids) != 1 || ids[0] != "8" {
		t.Fatalf("hole hidden: cp=%s ids=%v", checkpoint, ids)
	}
}

func TestRecoveryInvalidCheckpointIsRejected(t *testing.T) {
	fake := newFakeWS(t, nil)
	c := fake.client()
	c.opts.LoadCheckpoint = func(context.Context) (string, error) { return `{"version":1,"account":"OTHER","routes":{"0":3}}`, nil }
	connectTest(t, c, acceptingSink)
	eventually(t, func() bool { return c.Status().LastError == "RECOVERY_FAILED" })
	if c.Status().Recovery != "gap_detected" {
		t.Fatal(c.Status())
	}
}

func TestUnsupportedStreamKeepsLiveConnectionAndContiguousCheckpoint(t *testing.T) {
	var mu sync.Mutex
	checkpoint := `{"version":1,"account":"12345","routes":{"0":5}}`
	delivered := false
	diagnostics := []string{}
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.DiffResponse{Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: 5}}}})}})
	})
	c := fake.client()
	c.opts.RecoveryVerified = true
	c.opts.LoadCheckpoint = func(context.Context) (string, error) { mu.Lock(); defer mu.Unlock(); return checkpoint, nil }
	c.opts.OnDiagnostic = func(d Diagnostic) { mu.Lock(); defer mu.Unlock(); diagnostics = append(diagnostics, d.Code) }
	connectTest(t, c, func(_ context.Context, e domains.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if e.Checkpoint != "" {
			checkpoint = e.Checkpoint
		}
		if e.MessageID == "7" {
			delivered = true
		}
		return nil
	})
	eventually(t, func() bool { return c.Status().Recovery == "current" })
	fake.connections.Range(func(k, v any) bool {
		// Valid protobuf, but unknown peer category cannot be truthfully decoded.
		bad := marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 3, Id: 42}, Rid: 6, Date: 1720000000000}})
		fake.send(k.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Sequence: 6, Update: bad})}})
		fake.send(k.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Sequence: 7, Update: syntheticUpdate(t, 7)})}})
		return true
	})
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return delivered })
	mu.Lock()
	defer mu.Unlock()
	var cp recoveryCheckpoint
	_ = json.Unmarshal([]byte(checkpoint), &cp)
	if cp.Routes["0"] != 5 || !cp.Gap || c.Status().Transport != "connected" || c.Status().Recovery != "gap_detected" {
		t.Fatalf("unsupported update erased coverage/connection: cp=%s status=%+v", checkpoint, c.Status())
	}
	if len(diagnostics) != 1 || diagnostics[0] != "UPDATE_MESSAGE_INVALID" {
		t.Fatalf("unsafe/missing diagnostic: %v", diagnostics)
	}
}
