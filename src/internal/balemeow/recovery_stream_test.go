package balemeow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

// Exercise the regular WS consumer and restore a fresh client from an independent
// durable checkpoint, rather than mutating a connection's in-memory cursor.
func TestRecoveryStreamGapSurvivesRestart(t *testing.T) {
	for _, failMessage := range []bool{false, true} {
		name := "live_message_saved"
		if failMessage {
			name = "live_message_persistence_fails"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "checkpoint.json")
			if err := os.WriteFile(path, []byte(`{"version":1,"account":"12345","routes":{"0":5}}`), 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			load := func(context.Context) (string, error) {
				mu.Lock()
				defer mu.Unlock()
				b, err := os.ReadFile(path)
				return string(b), err
			}
			var delivered atomic.Bool
			sink := func(_ context.Context, e domains.Event) error {
				if e.Checkpoint != "" {
					mu.Lock()
					defer mu.Unlock()
					return os.WriteFile(path, []byte(e.Checkpoint), 0600)
				}
				if e.MessageID == "8" {
					if failMessage {
						return errors.New("synthetic persistence failure")
					}
					// The missing range must already be durable before this newer
					// message is accepted, even if storing the message later fails.
					raw, err := load(context.Background())
					var cp recoveryCheckpoint
					if err != nil || json.Unmarshal([]byte(raw), &cp) != nil || !cp.Gap {
						t.Errorf("new live message preceded durable gap: %s %v", raw, err)
					}
					delivered.Store(true)
				}
				return nil
			}
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				if m.Request.Method != "GetDiff" {
					t.Errorf("unexpected RPC %s", m.Request.Method)
					return
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.DiffResponse{Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: 5}}}})}})
			})
			c := fake.client()
			c.opts.LoadCheckpoint, c.opts.RecoveryVerified = load, true
			connectTest(t, c, sink)
			eventually(t, func() bool { return c.Status().Recovery == "current" })
			sendRecoveryStream(t, fake, 8)
			eventually(t, func() bool {
				s := c.Status()
				return s.Recovery == "gap_detected" && (!failMessage || s.Transport == "disconnected")
			})
			raw, err := load(context.Background())
			var cp recoveryCheckpoint
			if err != nil || json.Unmarshal([]byte(raw), &cp) != nil || !cp.Gap || cp.Routes["0"] != 5 {
				t.Fatalf("unresolved hole not retained: %s %v", raw, err)
			}
			if delivered.Load() == failMessage {
				t.Fatal("unexpected live-message delivery")
			}
			if err := c.Disconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			c2 := fake.client()
			c2.opts.LoadCheckpoint, c2.opts.RecoveryVerified = load, true
			connectTest(t, c2, sink)
			eventually(t, func() bool {
				r := c2.Status().Recovery
				return r == "current" || r == "gap_detected"
			})
			if c2.Status().Recovery != "gap_detected" {
				t.Fatal("restart erased an unresolved stream gap")
			}
		})
	}
}

func sendRecoveryStream(t *testing.T, fake *fakeWS, seq int32) {
	t.Helper()
	fake.connections.Range(func(k, v any) bool {
		fake.send(k.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Sequence: seq, Update: syntheticUpdate(t, int64(seq))})}})
		return true
	})
}

func TestRecoveryCompletedStreamCatchupFinishesStatus(t *testing.T) {
	for _, verified := range []bool{true, false} {
		want := "current"
		if !verified {
			want = "degraded"
		}
		t.Run(want, func(t *testing.T) {
			var requests atomic.Int32
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				seq := int32(5)
				var updates [][]byte
				if requests.Add(1) > 1 {
					seq = 8
					updates = [][]byte{syntheticUpdate(t, 6), syntheticUpdate(t, 7), syntheticUpdate(t, 8)}
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.DiffResponse{Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: seq}, Updates: updates}}})}})
			})
			c := fake.client()
			c.opts.RequestTimeout = time.Second
			c.opts.RecoveryVerified = verified
			c.opts.LoadCheckpoint = func(context.Context) (string, error) {
				return `{"version":1,"account":"12345","routes":{"0":5}}`, nil
			}
			checkpointPending := make(chan struct{})
			releaseCheckpoint := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseCheckpoint) }) }
			defer release()
			var initialAccepted, completed atomic.Bool
			connectTest(t, c, func(ctx context.Context, e domains.Event) error {
				if e.Checkpoint == "" {
					return nil
				}
				var cp recoveryCheckpoint
				if err := json.Unmarshal([]byte(e.Checkpoint), &cp); err != nil {
					return err
				}
				if cp.Routes["0"] == 8 {
					close(checkpointPending)
					select {
					case <-releaseCheckpoint:
						completed.Store(true)
					case <-ctx.Done():
						return ctx.Err()
					}
				} else {
					initialAccepted.Store(true)
				}
				return nil
			})
			eventually(t, func() bool { return initialAccepted.Load() && c.Status().Recovery == want })
			sendRecoveryStream(t, fake, 8)
			select {
			case <-checkpointPending:
			case <-time.After(time.Second):
				t.Fatal("catchup did not reach checkpoint persistence")
			}
			if c.Status().Recovery != "recovering" {
				t.Fatal("catchup advertised completion before checkpoint acceptance")
			}
			release()
			eventually(t, func() bool { return completed.Load() && c.Status().Recovery == want })
		})
	}
}

func TestRecoveryCheckpointFailureDoesNotAdvertiseCurrent(t *testing.T) {
	for _, gap := range []bool{false, true} {
		name := "catchup_checkpoint"
		if gap {
			name = "gap_checkpoint"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				seq := int32(5)
				var updates [][]byte
				if requests.Add(1) > 1 && !gap {
					seq = 8
					updates = [][]byte{syntheticUpdate(t, 6), syntheticUpdate(t, 7), syntheticUpdate(t, 8)}
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.DiffResponse{Routes: []*wire.RouteDiff{{State: &wire.RouteState{Group: &wire.PeerRef{}, Sequence: seq}, Updates: updates}}})}})
			})
			c := fake.client()
			c.opts.RecoveryVerified = true
			var mu sync.Mutex
			checkpoint := `{"version":1,"account":"12345","routes":{"0":5}}`
			c.opts.LoadCheckpoint = func(context.Context) (string, error) { mu.Lock(); defer mu.Unlock(); return checkpoint, nil }
			var delivered atomic.Bool
			connectTest(t, c, func(_ context.Context, e domains.Event) error {
				if e.MessageID == "8" {
					delivered.Store(true)
				}
				if e.Checkpoint == "" {
					return nil
				}
				var cp recoveryCheckpoint
				if err := json.Unmarshal([]byte(e.Checkpoint), &cp); err != nil {
					return err
				}
				if cp.Gap || cp.Routes["0"] == 8 {
					return errors.New("synthetic checkpoint persistence failure")
				}
				mu.Lock()
				checkpoint = e.Checkpoint
				mu.Unlock()
				return nil
			})
			eventually(t, func() bool { return c.Status().Recovery == "current" })
			sendRecoveryStream(t, fake, 8)
			eventually(t, func() bool { return c.Status().Transport == "disconnected" })
			if s := c.Status(); s.Recovery != "gap_detected" || s.LastError != "RECOVERY_FAILED" {
				t.Fatalf("checkpoint failure hidden: %+v", s)
			}
			mu.Lock()
			var cp recoveryCheckpoint
			_ = json.Unmarshal([]byte(checkpoint), &cp)
			mu.Unlock()
			if cp.Routes["0"] != 5 || cp.Gap {
				t.Fatal("failed checkpoint was reported accepted")
			}
			if gap && delivered.Load() {
				t.Fatal("live message was accepted after gap persistence failed")
			}
		})
	}
}

func TestRecoveryCoveredStreamDoesNotHideAnotherRouteGap(t *testing.T) {
	var requests atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		response := &wire.DiffResponse{Routes: []*wire.RouteDiff{
			{State: &wire.RouteState{Group: &wire.PeerRef{Id: 0}, Sequence: 5}},
			{State: &wire.RouteState{Group: &wire.PeerRef{Id: 1}, Sequence: 5}},
		}}
		if requests.Add(1) > 1 {
			response.Routes[0].State.Sequence = 8
			response.Routes[0].Updates = [][]byte{syntheticUpdate(t, 6), syntheticUpdate(t, 7), syntheticUpdate(t, 8)}
			response.Routes[1].TooLong = &wire.BoolValue{Value: true}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	c.opts.RecoveryVerified = true
	var mu sync.Mutex
	checkpoint := `{"version":1,"account":"12345","routes":{"0":5,"1":5}}`
	c.opts.LoadCheckpoint = func(context.Context) (string, error) { mu.Lock(); defer mu.Unlock(); return checkpoint, nil }
	connectTest(t, c, func(_ context.Context, e domains.Event) error {
		if e.Checkpoint != "" {
			mu.Lock()
			checkpoint = e.Checkpoint
			mu.Unlock()
		}
		return nil
	})
	eventually(t, func() bool { return c.Status().Recovery == "current" })
	sendRecoveryStream(t, fake, 8)
	eventually(t, func() bool { return c.Status().Recovery == "gap_detected" })
	mu.Lock()
	defer mu.Unlock()
	var cp recoveryCheckpoint
	_ = json.Unmarshal([]byte(checkpoint), &cp)
	if !cp.Gap || cp.Routes["0"] != 8 || cp.Routes["1"] != 5 {
		t.Fatalf("covered stream erased another route's hole: %s", checkpoint)
	}
}
