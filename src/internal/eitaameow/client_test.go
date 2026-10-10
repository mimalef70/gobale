package eitaameow

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func nativeFixture(t *testing.T, call func(string, object) (object, int)) (*Client, *httptest.Server) {
	t.Helper()
	codec, err := bundledCodec()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, maxWireBytes))
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		v, e := codec.decode(body, "EitaaObject")
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		outer := asObject(v)
		packed, _ := outer["packed_data"].([]byte)
		if len(packed) < 4 {
			t.Error("missing method")
			w.WriteHeader(500)
			return
		}
		id := binary.LittleEndian.Uint32(packed)
		var def definition
		for _, d := range codec.methods {
			if uint32(d.ID) == id {
				def = d
				break
			}
		}
		if def.Method == "" {
			t.Error("unknown method")
			w.WriteHeader(500)
			return
		}
		budget := 40000
		reader := &wireReader{data: packed, offset: 4, budget: &budget}
		params := object{}
		flags := map[string]uint32{}
		for _, p := range def.Params {
			typ := p.Type
			if typ == "#" {
				n, e := reader.u32()
				if e != nil {
					t.Error(e)
				}
				flags[p.Name] = n
				continue
			}
			if f, b, base, opt := optional(typ); opt {
				if flags[f]&(1<<b) == 0 {
					continue
				}
				typ = base
			}
			v, e := codec.decodeType(reader, typ, 0)
			if e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			params[p.Name] = v
		}
		result, status := call(def.Method, params)
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		var response bytes.Buffer
		if result.str("_") == "fixture.vector" {
			if err := codec.encodeType(&response, def.Type, result["items"], 0); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			w.Write(response.Bytes())
			return
		}
		d, ok := codec.names[result.str("_")]
		if !ok {
			t.Errorf("unknown result %s", result.str("_"))
			w.WriteHeader(500)
			return
		}
		if e := codec.encodeDefinition(&response, d, result, 0); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		w.Write(response.Bytes())
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestNativeOTPProducesPrivateVersionedSession(t *testing.T) {
	ctx := context.Background()
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		switch method {
		case "auth.sendCode":
			return object{"_": "auth.sentCode", "type": object{"_": "auth.sentCodeTypeSms", "length": 5}, "phone_code_hash": "synthetic-hash", "timeout": 60}, 200
		case "auth.signIn":
			if p.str("phone_code_hash") != "synthetic-hash" {
				t.Error("challenge hash changed")
			}
			return object{"_": "auth.authorization", "token": "synthetic-token", "user": object{"_": "user", "id": int64(42), "self": true}}, 200
		}
		t.Errorf("unexpected method %s", method)
		return nil, 500
	})
	ch, err := c.StartAuth(ctx, "989000000000")
	if err != nil {
		t.Fatal(err)
	}
	if ch.ResendAfterSeconds == nil || *ch.ResendAfterSeconds != 60 {
		t.Fatal("cooldown absent")
	}
	c.SetSessionPersister(func(context.Context, *domains.Session) error {
		t.Fatal("initial auth must be saved by the gateway, not the rotation callback")
		return nil
	})
	s, err := c.SubmitCode(ctx, ch.ID, "12345")
	if err != nil {
		t.Fatal(err)
	}
	if s.UserID != "42" || len(s.Data) == 0 {
		t.Fatal("missing private session")
	}
	if err = (Contract{}).ValidateSession(s); err != nil {
		t.Fatal(err)
	}
}

func TestPeerSessionUpdateMustCommitBeforeUse(t *testing.T) {
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	c.session.UserID = "42"
	c.session.Token = "synthetic"
	failure := errors.New("storage unavailable")
	c.SetSessionPersister(func(context.Context, *domains.Session) error { return failure })
	response := object{"users": []any{object{"_": "user", "id": int64(90), "access_hash": int64(123)}}}
	if err = c.rememberEntities(context.Background(), response); !errors.Is(err, failure) {
		t.Fatalf("session failure ignored: %v", err)
	}
	if c.session.Peers["user:90"] != nil {
		t.Fatal("uncommitted metadata activated")
	}
}
func TestNativeSendNeverRetriesAmbiguousPost(t *testing.T) {
	var calls atomic.Int32
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method != "messages.sendMessage" {
			t.Fatalf("unexpected method %s", method)
		}
		calls.Add(1)
		if p.num("random_id") != 987654321 {
			t.Error("persisted request identity replaced")
		}
		return nil, 500
	})
	c.session.Token = "synthetic-token"
	c.session.UserID = "42"
	_, err := c.Send(context.Background(), domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Kind: "text", Text: "synthetic", RequestID: "987654321"})
	var e *domains.Error
	if !errors.As(err, &e) || !e.Ambiguous || calls.Load() != 1 {
		t.Fatalf("unsafe retry/result: calls=%d err=%v", calls.Load(), err)
	}
}
func TestNativeDifferenceFailureDoesNotAdvance(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method != "updates.getDifference" || p.num("pts") != 10 {
			t.Fatalf("unexpected request: %s %#v", method, p)
		}
		return object{"_": "updates.difference", "new_messages": []object{{"_": "message", "id": 15, "peer_id": object{"_": "peerUser", "user_id": 99}, "from_id": object{"_": "peerUser", "user_id": 99}, "date": 100, "message": "synthetic"}}, "new_encrypted_messages": []object{}, "other_updates": []object{}, "chats": []object{}, "users": []object{}, "state": object{"_": "updates.state", "pts": 11, "qts": 0, "date": 100, "seq": 1, "unread_count": 0}}, 200
	})
	c.session.Token = "synthetic-token"
	c.session.UserID = "42"
	cp := checkpoint{Version: 1, Account: "42", PTS: 10, Date: 99}
	raw := checkpointJSON(cp)
	failure := errors.New("commit refused")
	failed, err := c.pollPage(context.Background(), raw, cp, func(_ context.Context, b domains.EventBatch) error {
		if len(b.Events) != 1 || len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != raw {
			t.Fatal("page not atomic")
		}
		return failure
	})
	if !errors.Is(err, failure) || failed != cp {
		t.Fatalf("checkpoint advanced despite failed commit: %#v %v", failed, err)
	}
	var accepted domains.EventBatch
	next, err := c.pollPage(context.Background(), raw, cp, func(_ context.Context, b domains.EventBatch) error { accepted = b; return nil })
	if err != nil || next.PTS != 11 {
		t.Fatalf("retry failed: %#v %v", next, err)
	}
	data, _ := json.Marshal(accepted.Events)
	if bytes.Contains(data, []byte("synthetic-token")) || bytes.Contains(data, []byte("access_hash")) {
		t.Fatal("private metadata leaked")
	}
}
func TestUnknownOperationRejectedBeforeProvider(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("unexpected provider request"); return nil, 500 })
	if _, err := c.Call(context.Background(), "auth.resetAuthorizations", json.RawMessage(`{}`)); err == nil {
		t.Fatal("arbitrary RPC accepted")
	}
	if _, _, err := (Contract{}).NormalizeOperation("message.read", json.RawMessage(`{"peer":{"type":"user","id":"42"},"message_id":"1","message_id":"2"}`)); err == nil {
		t.Fatal("duplicate JSON accepted")
	}
}
