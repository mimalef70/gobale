package eitaameow

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/workpool"
)

func TestRunConcurrent100NativeClients(t *testing.T) {
	if os.Getenv("GOOMNI_MIXED_CAPACITY") != "1" {
		t.Skip("set GOOMNI_MIXED_CAPACITY=1 for 100 synthetic native Eitaa accounts")
	}
	runNativeClients(t, 100)
}
func TestNativePollingIndependentRequestLifetime(t *testing.T) { runNativeClients(t, 3) }

func fixtureDecodeMethod(c *codec, raw []byte) (string, object, error) {
	if len(raw) < 4 {
		return "", nil, errors.New("missing method")
	}
	id := binary.LittleEndian.Uint32(raw)
	var def definition
	for _, d := range c.methods {
		if uint32(d.ID) == id {
			def = d
			break
		}
	}
	if def.Method == "" {
		return "", nil, errors.New("unknown method")
	}
	budget := 40000
	r := &wireReader{data: raw, offset: 4, budget: &budget}
	flags := map[string]uint32{}
	params := object{}
	for _, p := range def.Params {
		typ := p.Type
		if typ == "#" {
			n, e := r.u32()
			if e != nil {
				return "", nil, e
			}
			flags[p.Name] = n
			continue
		}
		if flag, bit, base, opt := optional(typ); opt {
			if flags[flag]&(1<<bit) == 0 {
				continue
			}
			typ = base
		}
		value, e := c.decodeType(r, typ, 0)
		if e != nil {
			return "", nil, e
		}
		params[p.Name] = value
	}
	if r.offset != len(raw) {
		return "", nil, errors.New("unconsumed request")
	}
	return def.Method, params, nil
}
func runNativeClients(t *testing.T, count int) {
	t.Helper()
	codec, err := bundledCodec()
	if err != nil {
		t.Fatal(err)
	}
	var rpcs, logins, updates, checkpoints atomic.Int64
	var target atomic.Int64
	target.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, maxWireBytes+1))
		if e != nil || len(raw) > maxWireBytes {
			w.WriteHeader(400)
			return
		}
		outer, e := codec.decode(raw, "EitaaObject")
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		envelope := asObject(outer)
		packed, ok := envelope["packed_data"].([]byte)
		if !ok {
			w.WriteHeader(400)
			return
		}
		method, p, e := fixtureDecodeMethod(codec, packed)
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		rpcs.Add(1)
		var result object
		if method == "auth.sendCode" || method == "auth.signIn" {
			index, e := strconv.Atoi(strings.TrimPrefix(p.str("phone_number"), "990000"))
			if e != nil || index < 0 || index >= count || envelope.str("token") != "" {
				t.Error("invalid synthetic auth scope")
				w.WriteHeader(400)
				return
			}
			if method == "auth.sendCode" {
				result = object{"_": "auth.sentCode", "type": object{"_": "auth.sentCodeTypeSms", "length": 5}, "phone_code_hash": fmt.Sprintf("fixture-%d", index), "timeout": 60}
			} else {
				if p.str("phone_code_hash") != fmt.Sprintf("fixture-%d", index) || p.str("phone_code") != "12345" {
					t.Error("mixed auth challenge")
					w.WriteHeader(400)
					return
				}
				logins.Add(1)
				result = object{"_": "auth.authorization", "token": fmt.Sprintf("fixture-token-%d", index), "user": object{"_": "user", "id": 1000 + index, "self": true}}
			}
		} else {
			token := envelope.str("token")
			index, e := strconv.Atoi(strings.TrimPrefix(token, "fixture-token-"))
			if e != nil || index < 0 || index >= count || !strings.HasPrefix(token, "fixture-token-") {
				t.Error("invalid synthetic session scope")
				w.WriteHeader(401)
				return
			}
			switch method {
			case "updates.getState":
				result = object{"_": "updates.state", "pts": 0, "qts": 0, "date": 1700000000, "seq": 0, "unread_count": 0}
			case "updates.getDifference":
				pts := p.num("pts")
				if pts < 0 || pts > target.Load() {
					t.Error("checkpoint rewound or foreign")
					w.WriteHeader(400)
					return
				}
				if pts < target.Load() {
					next := pts + 1
					result = object{"_": "updates.difference", "new_messages": []object{{"_": "message", "id": next, "peer_id": object{"_": "peerUser", "user_id": 1000 + index}, "from_id": object{"_": "peerUser", "user_id": 1000 + index}, "date": 1700000000 + next, "message": fmt.Sprintf("synthetic-account-%d", index)}}, "new_encrypted_messages": []object{}, "other_updates": []object{}, "chats": []object{}, "users": []object{}, "state": object{"_": "updates.state", "pts": next, "qts": 0, "date": 1700000000 + next, "seq": next, "unread_count": 0}}
				} else {
					result = object{"_": "updates.differenceEmpty", "date": 1700000000 + pts, "seq": pts}
				}
			case "messages.getDialogs":
				result = object{"_": "messages.dialogs", "dialogs": []object{}, "messages": []object{}, "chats": []object{}, "users": []object{}}
			default:
				t.Errorf("unexpected RPC %s", method)
				w.WriteHeader(400)
				return
			}
		}
		var body bytes.Buffer
		if e = codec.encodeDefinition(&body, codec.names[result.str("_")], result, 0); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(body.Bytes())
	}))
	defer server.Close()
	pool := workpool.New(4)
	pool.SetProviders([]domains.Provider{domains.ProviderEitaa})
	var inFlight, maxFlight atomic.Int64
	permit := func(ctx context.Context, connection string) (func(), error) {
		release, e := pool.AcquireFor(ctx, domains.ProviderEitaa, connection)
		if e != nil {
			return nil, e
		}
		n := inFlight.Add(1)
		for old := maxFlight.Load(); n > old && !maxFlight.CompareAndSwap(old, n); old = maxFlight.Load() {
		}
		var once sync.Once
		return func() { once.Do(func() { inFlight.Add(-1); release() }) }, nil
	}
	sessions := make([]*domains.Session, count)
	clients := make([]*Client, count)
	state := make([]map[string]string, count)
	locks := make([]sync.Mutex, count)
	for index := range count {
		i := index
		state[i] = map[string]string{}
		connection := fmt.Sprintf("eitaa-fixture-%d", i)
		c, e := New(Config{Endpoint: server.URL, HTTPClient: server.Client(), ConnectionID: connection, PollInterval: time.Hour, PollPermit: func(ctx context.Context) (func(), error) { return permit(ctx, connection) }, LoadCheckpoint: func(_ context.Context, scope string) (string, error) {
			locks[i].Lock()
			defer locks[i].Unlock()
			return state[i][scope], nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		clients[i] = c
		defer c.Disconnect(context.Background())
	}
	for pass := 0; pass < 2; pass++ {
		target.Store(int64(pass + 1))
		start := time.Now()
		var wg sync.WaitGroup
		for index := range count {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				release, e := permit(ctx, fmt.Sprintf("eitaa-fixture-%d", i))
				if e != nil {
					t.Error(e)
					return
				}
				defer release()
				if sessions[i] == nil {
					ch, e := clients[i].StartAuth(ctx, fmt.Sprintf("990000%06d", i))
					if e != nil {
						t.Error(e)
						return
					}
					sessions[i], e = clients[i].SubmitCode(ctx, ch.ID, "12345")
					if e != nil {
						t.Error(e)
						return
					}
				}
				e = clients[i].ConnectBatch(ctx, sessions[i], func(_ context.Context, b domains.EventBatch) error {
					locks[i].Lock()
					defer locks[i].Unlock()
					for _, cp := range b.Checkpoints {
						if state[i][cp.Scope] != cp.Expected {
							return errors.New("checkpoint CAS conflict")
						}
						parsed, e := parseCheckpoint(cp.Next, strconv.Itoa(1000+i))
						if e != nil {
							return e
						}
						if old := state[i][cp.Scope]; old != "" {
							previous, e := parseCheckpoint(old, strconv.Itoa(1000+i))
							if e != nil || parsed.PTS < previous.PTS {
								return errors.New("checkpoint rewind")
							}
						}
					}
					for _, event := range b.Events {
						if event.Provider != domains.ProviderEitaa {
							return errors.New("provider mismatch")
						}
						if event.Type == "message" {
							if event.Peer.ID != strconv.Itoa(1000+i) || event.Message == nil || event.Message.Body != fmt.Sprintf("synthetic-account-%d", i) {
								return errors.New("cross-account event")
							}
							updates.Add(1)
						}
					}
					for _, cp := range b.Checkpoints {
						state[i][cp.Scope] = cp.Next
						checkpoints.Add(1)
					}
					return nil
				})
				if e != nil {
					t.Error(e)
				}
			}(index)
		}
		wg.Wait()
		deadline := time.Now().Add(15 * time.Second)
		expected := int64((pass + 1) * count)
		for updates.Load() < expected && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if updates.Load() != expected {
			t.Fatalf("pass=%d updates=%d want=%d", pass, updates.Load(), expected)
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
	if maxFlight.Load() > 4 || pool.Active() != 0 || logins.Load() != int64(count) {
		t.Fatalf("permits=%d active=%d logins=%d", maxFlight.Load(), pool.Active(), logins.Load())
	}
	for i := range count {
		locks[i].Lock()
		cp, e := parseCheckpoint(state[i][accountScope], strconv.Itoa(1000+i))
		locks[i].Unlock()
		if e != nil || cp.PTS != 2 {
			t.Fatalf("account %d checkpoint %#v err=%v", i, cp, e)
		}
	}
	t.Logf("synthetic-native Eitaa: accounts=%d authenticated=%d connections=%d RPCs=%d updates=%d checkpoint_commits=%d max_permits=%d; live accounts untested", count, logins.Load(), count*2, rpcs.Load(), updates.Load(), checkpoints.Load(), maxFlight.Load())
}
