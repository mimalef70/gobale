package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

// A child owns the actual SQLite connection and dies without Close/rollback.
// Marker files contain only synthetic IDs. These are process crash tests, not
// graceful close/open tests, and also run with the shipped purego SQLite driver.
func TestProcessCrashDurableBoundaries(t *testing.T) {
	for _, phase := range []string{"enqueue", "claim", "provider_observed", "receiver_committed", "schedule_materialized", "schedule_transactions", "batch_uncommitted", "batch_committed", "batch_transactions"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			command := exec.Command(os.Args[0], "-test.run=^TestProcessCrashChild$", "-test.count=1")
			command.Env = append(os.Environ(), "GOOMNI_CRASH_CHILD="+phase, "GOOMNI_CRASH_ROOT="+root)
			log, err := os.Create(filepath.Join(root, "child.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command.Stdout = log
			command.Stderr = log
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			waited := false // owned only by the parent goroutine
			defer func() {
				if !waited {
					_ = command.Process.Kill()
					<-done
				}
			}()
			deadline := time.Now().Add(15 * time.Second)
			for {
				if _, e := os.Stat(filepath.Join(root, "ready")); e == nil {
					break
				}
				select {
				case e := <-done:
					waited = true
					b, _ := os.ReadFile(filepath.Join(root, "child.log"))
					t.Fatalf("child exited before marker: %v %s", e, b)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("child marker timeout")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if phase == "schedule_transactions" || phase == "batch_transactions" {
				time.Sleep(15 * time.Millisecond)
			}
			if err = command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			<-done
			waited = true
			st, err := Open(filepath.Join(root, "gateway.db"), []byte(strings.Repeat("x", 32)))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			d, err := st.GetDevice(context.Background(), "crash-account")
			if err != nil {
				t.Fatal(err)
			}
			switch phase {
			case "batch_uncommitted", "batch_committed", "batch_transactions":
				cp, e := st.ScopedCheckpoint(context.Background(), d.ConnectionID, "account")
				if e != nil {
					t.Fatal(e)
				}
				pages := 0
				if cp != "" {
					pages, e = strconv.Atoi(cp)
					if e != nil {
						t.Fatal(e)
					}
				}
				var events, deliveries int
				if e = st.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); e != nil {
					t.Fatal(e)
				}
				if e = st.db.QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&deliveries); e != nil {
					t.Fatal(e)
				}
				if events != pages*2 || deliveries != events || (phase == "batch_uncommitted" && pages != 0) || (phase != "batch_uncommitted" && pages == 0) {
					t.Fatalf("non-atomic batch: pages=%d events=%d deliveries=%d", pages, events, deliveries)
				}
			case "enqueue", "claim", "provider_observed":
				var state, rid string
				if err = st.db.QueryRow(`SELECT state,json_extract(request,'$.request_id') FROM operations`).Scan(&state, &rid); err != nil {
					t.Fatal(err)
				}
				wanted := "unknown"
				if phase == "enqueue" {
					wanted = "queued"
				}
				if state != wanted || rid == "" {
					t.Fatalf("state=%s RIDempty=%v", state, rid == "")
				}
				if phase == "provider_observed" {
					observed, e := os.ReadFile(filepath.Join(root, "provider-rid"))
					if e != nil || string(observed) != rid {
						t.Fatal("provider did not observe persisted RID")
					}
				}
				jobs, e := st.ClaimOperations(context.Background(), 10)
				if e != nil {
					t.Fatal(e)
				}
				if wanted == "unknown" && len(jobs) != 0 {
					t.Fatal("ambiguous work became eligible for automatic resend")
				}
			case "receiver_committed":
				var id, body, state string
				if err = st.db.QueryRow(`SELECT event_id,body,state FROM deliveries`).Scan(&id, &body, &state); err != nil {
					t.Fatal(err)
				}
				var recorded domains.Event
				data, e := os.ReadFile(filepath.Join(root, "receiver-event"))
				if e != nil || json.Unmarshal(data, &recorded) != nil || recorded.ID != id || string(data) != body {
					t.Fatal("webhook identity/body changed across crash")
				}
				if state != "retry" {
					t.Fatalf("delivery state after crash=%s", state)
				}
				cp, e := st.Checkpoint(context.Background(), d.ConnectionID)
				if e != nil || cp != "synthetic-checkpoint" {
					t.Fatalf("checkpoint=%q err=%v", cp, e)
				}
			case "schedule_materialized", "schedule_transactions":
				var count, ops, links int64
				if err = st.db.QueryRow(`SELECT COALESCE(SUM(occurrence_count),0) FROM schedules`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if err = st.db.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&ops); err != nil {
					t.Fatal(err)
				}
				if err = st.db.QueryRow(`SELECT COUNT(*) FROM schedule_occurrences`).Scan(&links); err != nil {
					t.Fatal(err)
				}
				if count == 0 || count != ops || ops != links {
					t.Fatalf("non-atomic occurrence: counters=%d operations=%d links=%d", count, ops, links)
				}
			}
			var integrity string
			if err = st.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity %q %v", integrity, err)
			}
		})
	}
}

func TestProcessCrashChild(t *testing.T) {
	phase := os.Getenv("GOOMNI_CRASH_CHILD")
	if phase == "" {
		t.Skip("subprocess fixture")
	}
	root := os.Getenv("GOOMNI_CRASH_ROOT")
	ctx := context.Background()
	st, err := Open(filepath.Join(root, "gateway.db"), []byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	provider := domains.ProviderBale
	if strings.HasPrefix(phase, "batch_") {
		provider = domains.ProviderEitaa
	}
	d, err := st.CreateDevice(ctx, "crash-account", provider)
	if err != nil {
		t.Fatal(err)
	}
	mark := func(name string, data []byte) {
		f, e := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write(data); e != nil {
			t.Fatal(e)
		}
		if e = f.Sync(); e != nil {
			t.Fatal(e)
		}
		if e = f.Close(); e != nil {
			t.Fatal(e)
		}
	}
	request := domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "77"}, Kind: "text", Text: "synthetic crash payload"}
	switch phase {
	case "batch_uncommitted", "batch_committed", "batch_transactions":
		for i := 0; ; i++ {
			first, second := event(fmt.Sprintf("page-%d-first", i), ""), event(fmt.Sprintf("page-%d-second", i), "")
			expected := ""
			if i > 0 {
				expected = strconv.Itoa(i)
			}
			batch := domains.EventBatch{Events: []domains.Event{first, second}, Checkpoints: []domains.CheckpointTransition{{Scope: "account", Expected: expected, Next: strconv.Itoa(i + 1)}}}
			targets := []WebhookTarget{{URL: "https://synthetic.invalid/hook", Secret: "synthetic"}}
			if phase == "batch_uncommitted" {
				tx, e := st.beginTx(ctx)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = st.appendEventTx(ctx, tx, d, first, targets, false); e != nil {
					t.Fatal(e)
				}
				if e = setCheckpointTx(ctx, tx, d.ConnectionID, "account", "1"); e != nil {
					t.Fatal(e)
				}
				mark("ready", []byte("ready"))
				select {}
			}
			if _, e := st.AppendBatch(ctx, d.ConnectionID, batch, targets); e != nil {
				t.Fatal(e)
			}
			if i == 0 {
				mark("ready", []byte("ready"))
			}
			if phase == "batch_committed" {
				select {}
			}
			if i > 10000 {
				t.Fatal("parent did not kill provider batch loop")
			}
		}
	case "enqueue", "claim", "provider_observed":
		op, _, e := st.Enqueue(ctx, d.ConnectionID, request, "synthetic-key", AdmissionLimits{Global: 1000})
		if e != nil {
			t.Fatal(e)
		}
		if phase != "enqueue" {
			jobs, e := st.ClaimOperations(ctx, 1)
			if e != nil || len(jobs) != 1 {
				t.Fatalf("claim %v %v", jobs, e)
			}
		}
		if phase == "provider_observed" {
			mark("provider-rid", []byte(op.Request.RequestID))
		}
	case "receiver_committed":
		_, e := st.AppendEvent(ctx, d.ConnectionID, domains.Event{ID: "synthetic-event", Type: "message", Peer: request.Peer, Time: time.Now().UTC(), Payload: json.RawMessage(`{"kind":"text","message":"synthetic"}`), Checkpoint: "synthetic-checkpoint"}, []WebhookTarget{{URL: "https://synthetic.invalid/webhook", Secret: "synthetic-secret"}})
		if e != nil {
			t.Fatal(e)
		}
		// Claim using the real storage method; persist a receiver ledger before the
		// gateway can record successful delivery, then let the parent kill us.
		jobs, e := st.ClaimDeliveries(ctx, 1, time.Now())
		if e != nil || len(jobs) != 1 {
			t.Fatalf("claim delivery %v %v", jobs, e)
		}
		mark("receiver-event", jobs[0].Body)
	case "schedule_materialized":
		when := time.Now().UTC()
		s, e := st.CreateSchedule(ctx, d.ConnectionID, request, when)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = st.MaterializeSchedule(ctx, d.ConnectionID, s.ID, when, nil, AdmissionLimits{Global: 1000}); e != nil {
			t.Fatal(e)
		}
	case "schedule_transactions":
		for i := 0; ; i++ {
			when := time.Now().UTC()
			s, e := st.CreateSchedule(ctx, d.ConnectionID, request, when)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.MaterializeSchedule(ctx, d.ConnectionID, s.ID, when, nil, AdmissionLimits{Global: 100000, Connection: 100000}); e != nil {
				t.Fatal(e)
			}
			if i == 0 {
				mark("ready", []byte("ready"))
			}
			if i > 10000 {
				t.Fatal(fmt.Errorf("parent did not kill transaction loop"))
			}
		}
	default:
		t.Fatal("unknown fixture phase")
	}
	mark("ready", []byte("ready"))
	select {}
}
