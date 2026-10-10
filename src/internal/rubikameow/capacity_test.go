package rubikameow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunConcurrent100NativeClients(t *testing.T) {
	if os.Getenv("GOOMNI_MIXED_CAPACITY") != "1" {
		t.Skip("set GOOMNI_MIXED_CAPACITY=1 for 100 synthetic native Rubika accounts")
	}
	runNativeClients(t, 100)
}
func TestNativeSocketIndependentRequestLifetime(t *testing.T) { runNativeClients(t, 3) }
func runNativeClients(t *testing.T, count int) {
	t.Helper()
	authUsers := map[string]string{}
	sessions := make([]*domains.Session, count)
	clients := make([]*Client, count)
	_, private, e := createKeys()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < count; i++ {
		auth, e := randomAuth()
		if e != nil {
			t.Fatal(e)
		}
		user := fmt.Sprintf("u0synthetic%d", i)
		authUsers[auth] = user
		raw, _ := json.Marshal(privateSession{Auth: auth, PrivateKey: private, UserID: user})
		sessions[i] = &domains.Session{Provider: domains.ProviderRubika, Version: 1, UserID: user, Data: raw}
	}
	var handshakes, rpcs, updates, checkpoints atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/socket" {
			conn, e := websocket.Accept(w, r, nil)
			if e != nil {
				return
			}
			defer conn.CloseNow()
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			_, raw, e := conn.Read(ctx)
			if e != nil {
				return
			}
			h, e := jsonObject(raw)
			if e != nil || h.str("method") != "handShake" || authUsers[h.str("auth")] == "" {
				t.Error("invalid socket handshake")
				return
			}
			handshakes.Add(1)
			user := authUsers[h.str("auth")]
			key, _ := authKey(h.str("auth"))
			encoded, _ := encrypt(object{"user_guid": user, "message_updates": []any{object{"action": "New", "object_guid": "u0peer", "message_id": "9007199254740993", "message": object{"message_id": "9007199254740993", "time": json.Number("1700000000"), "text": "synthetic", "type": "Text", "author_object_guid": "u0peer"}}}}, key)
			frame, _ := json.Marshal(object{"type": "messenger", "data_enc": encoded})
			if e = conn.Write(ctx, websocket.MessageText, frame); e != nil {
				return
			}
			for {
				_, _, e = conn.Read(ctx)
				if e != nil {
					return
				}
			}
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, maxPayload+1))
		outer, e := jsonObject(raw)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		auth := transformAuth(outer.str("auth"))
		user := authUsers[auth]
		if user == "" {
			w.WriteHeader(401)
			return
		}
		key, _ := authKey(auth)
		call, e := decrypt(outer.str("data_enc"), key)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		rpcs.Add(1)
		var data object
		switch call.str("method") {
		case "getUserInfo":
			if asObject(call["input"]).str("user_guid") != user {
				t.Error("cross-account RPC")
			}
			data = object{"user": object{"user_guid": user}}
		case "getChats":
			data = object{"state": "100", "chats": []any{}, "has_continue": false}
		case "getChatsUpdates":
			data = object{"new_state": "101", "chats": []any{}}
		default:
			t.Errorf("unexpected RPC %s", call.str("method"))
			w.WriteHeader(400)
			return
		}
		encrypted, _ := encrypt(object{"status": "OK", "data": data}, key)
		_ = json.NewEncoder(w).Encode(object{"data_enc": encrypted})
	}))
	defer server.Close()
	sem := make(chan struct{}, 4)
	var inFlight, maxFlight atomic.Int64
	permit := func(ctx context.Context) (func(), error) {
		select {
		case sem <- struct{}{}:
			n := inFlight.Add(1)
			for old := maxFlight.Load(); n > old && !maxFlight.CompareAndSwap(old, n); old = maxFlight.Load() {
			}
			return func() { inFlight.Add(-1); <-sem }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	state := make([]map[string]string, count)
	locks := make([]sync.Mutex, count)
	for i := range count {
		state[i] = map[string]string{}
		index := i
		c, e := New(Config{ConnectionID: fmt.Sprintf("connection%d", i), APIEndpoint: server.URL, SocketEndpoint: "ws" + strings.TrimPrefix(server.URL, "http") + "/socket", HTTPClient: server.Client(), PollPermit: permit, LoadCheckpoint: func(_ context.Context, scope string) (string, error) {
			locks[index].Lock()
			defer locks[index].Unlock()
			return state[index][scope], nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		clients[i] = c
		defer c.Disconnect(context.Background())
	}
	for pass := 0; pass < 2; pass++ {
		var wg sync.WaitGroup
		start := time.Now()
		for i := range count {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				release, e := permit(context.Background())
				if e != nil {
					t.Error(e)
					return
				}
				defer release()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				e = clients[index].ConnectBatch(ctx, sessions[index], func(_ context.Context, b domains.EventBatch) error {
					locks[index].Lock()
					defer locks[index].Unlock()
					for _, cp := range b.Checkpoints {
						if state[index][cp.Scope] != cp.Expected {
							return errors.New("CAS conflict")
						}
						var parsed checkpoint
						if json.Unmarshal([]byte(cp.Next), &parsed) != nil || parsed.Account != sessions[index].UserID {
							return errors.New("account mismatch")
						}
						state[index][cp.Scope] = cp.Next
						checkpoints.Add(1)
					}
					for _, event := range b.Events {
						if event.Provider != domains.ProviderRubika {
							return errors.New("provider mismatch")
						}
						if event.Type == "message" {
							if event.MessageID != "9007199254740993" || event.Message.Body != "synthetic" {
								return errors.New("bad message")
							}
							updates.Add(1)
						}
					}
					return nil
				})
				cancel()
				if e != nil {
					t.Error(e)
				}
			}(i)
		}
		wg.Wait()
		deadline := time.Now().Add(10 * time.Second)
		target := int64((pass + 1) * count)
		for (updates.Load() < target || checkpoints.Load() < target) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if updates.Load() != target || checkpoints.Load() < target {
			t.Fatalf("pass=%d updates=%d checkpoints=%d target=%d", pass, updates.Load(), checkpoints.Load(), target)
		}
		for _, c := range clients {
			if e := c.Disconnect(context.Background()); e != nil {
				t.Fatal(e)
			}
		}
		if time.Since(start) > 2*time.Minute {
			t.Fatal("reconnect budget exceeded")
		}
	}
	if maxFlight.Load() > 4 || handshakes.Load() != int64(count*2) {
		t.Fatalf("concurrency=%d handshakes=%d", maxFlight.Load(), handshakes.Load())
	}
	t.Logf("synthetic-native Rubika: accounts=%d handshakes=%d RPCs=%d updates=%d checkpoint_commits=%d max_permits=%d; live accounts untested", count, handshakes.Load(), rpcs.Load(), updates.Load(), checkpoints.Load(), maxFlight.Load())
}
