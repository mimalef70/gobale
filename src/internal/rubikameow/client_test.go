package rubikameow

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/mimalef70/goomni/src/domains"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureAuth = "abcdefghijklmnopqrstuvwxyzabcdef"

func TestLiteralCryptoAndBoundedJSON(t *testing.T) {
	key, e := authKey(fixtureAuth)
	if e != nil || string(key) != "zabcdefgjklmnopqhijklmnorstuvwxy" {
		t.Fatalf("key: %q %v", key, e)
	}
	// AES fixture generated independently by OpenSSL, with a non-JavaScript-safe ID.
	o, e := decrypt("aBBMfnsfLivZM3qCein+4V0sJSn4iCw0YFAt3kGVQOUK8D6R06A9TgmZ1CcCiZAz", key)
	if e != nil || o.str("text") != "ب" || o.str("n") != "9007199254740993" {
		t.Fatalf("literal decode: %v %v", o, e)
	}
	for _, raw := range []string{`{"a":1,"\u0061":2}`, `{"x":1} {}`, strings.Repeat(`{"x":`, 50) + `0` + strings.Repeat(`}`, 50), "{\"x\":\"\xff\"}"} {
		if _, e = jsonObject([]byte(raw)); e == nil {
			t.Fatal("unsafe JSON accepted")
		}
	}
	for _, raw := range []string{"", "AQ==", strings.Repeat("A", 24), "not-base64"} {
		if _, e = decrypt(raw, key); e == nil {
			t.Fatal("bad ciphertext accepted")
		}
	}
	if got := transformAuth("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"); got != "gfedcbazyxwvutsrqponmlkjihDCBAZYXWVUTSRQPONMLKJIHGFE3210987654" {
		t.Fatalf("transform literal: %s", got)
	}
}

type rpcFixture func(method string, input object, anonymous bool) object

func newRPCFixture(t *testing.T, fn rpcFixture) (*Client, *httptest.Server) {
	t.Helper()
	_, private, e := createKeys()
	if e != nil {
		t.Fatal(e)
	}
	key, e := parsePrivateKey(private)
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, maxPayload+1))
		outer, e := jsonObject(raw)
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		auth := fixtureAuth
		tmp := outer.str("tmp_session")
		if tmp != "" {
			auth = tmp
		} else {
			signature, err := base64.StdEncoding.DecodeString(outer.str("sign"))
			digest := sha256.Sum256([]byte(outer.str("data_enc")))
			if err != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
				t.Error("authenticated request RSA signature invalid")
				w.WriteHeader(401)
				return
			}
		}
		aesKey, _ := authKey(auth)
		call, e := decrypt(outer.str("data_enc"), aesKey)
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		data := fn(call.str("method"), asObject(call["input"]), tmp != "")
		if data == nil {
			w.WriteHeader(500)
			return
		}
		reply := object{"status": "OK", "status_det": "OK", "data": data}
		if failure := asObject(data["_fixture_error"]); failure != nil {
			reply = failure
		}
		encrypted, e := encrypt(reply, aesKey)
		if e != nil {
			t.Error(e)
		}
		_ = json.NewEncoder(w).Encode(object{"data_enc": encrypted})
	}))
	t.Cleanup(srv.Close)
	c, e := New(Config{APIEndpoint: srv.URL, SocketEndpoint: "ws" + strings.TrimPrefix(srv.URL, "http"), HTTPClient: srv.Client(), ConnectionID: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	c.session = privateSession{Auth: fixtureAuth, UserID: "u0self", PrivateKey: private}
	c.key = key
	return c, srv
}
func TestPasswordBeforeOTPAndPrivateSession(t *testing.T) {
	var calls atomic.Int32
	c, _ := newRPCFixture(t, func(method string, input object, anonymous bool) object {
		if !anonymous {
			t.Error("login was not temporary")
		}
		calls.Add(1)
		switch method {
		case "sendCode":
			if input.str("pass_key") == "" {
				return object{"status": "SendPassKey"}
			}
			if input.str("pass_key") != "fixture-password" {
				t.Error("password missing")
			}
			return object{"status": "OK", "phone_code_hash": "private-provider-hash", "send_code_timeout": json.Number("45")}
		case "signIn":
			publicRaw, e := base64.StdEncoding.DecodeString(transformAuth(input.str("public_key")))
			if e != nil {
				t.Fatal(e)
			}
			block, _ := pem.Decode(publicRaw)
			if block == nil {
				t.Fatal("missing PEM")
			}
			pub, e := x509.ParsePKIXPublicKey(block.Bytes)
			if e != nil {
				t.Fatal(e)
			}
			auth, e := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub.(*rsa.PublicKey), []byte(fixtureAuth), nil)
			if e != nil {
				t.Fatal(e)
			}
			return object{"status": "OK", "auth": base64.StdEncoding.EncodeToString(auth), "user": object{"user_guid": "u0self"}}
		}
		t.Errorf("unexpected %s", method)
		return nil
	})
	c.SetSessionPersister(func(context.Context, *domains.Session) error {
		t.Error("initial auth must be saved by usecase")
		return errors.New("unexpected")
	})
	challenge, e := c.StartAuth(context.Background(), "+999123456789")
	if e != nil || challenge.State != "awaiting_password" {
		t.Fatalf("challenge: %+v %v", challenge, e)
	}
	session, e := c.SubmitPassword(context.Background(), challenge.ID, "fixture-password")
	if e != nil || session != nil || c.CurrentChallenge().State != "awaiting_code" {
		t.Fatalf("password continuation: %v %v", session, e)
	}
	session, e = c.SubmitCode(context.Background(), challenge.ID, "12345")
	if e != nil {
		t.Fatal(e)
	}
	if e = (Contract{}).ValidateSession(session); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls %d", calls.Load())
	}
	raw, _ := json.Marshal(c.CurrentChallenge())
	if strings.Contains(string(raw), "private-provider-hash") {
		t.Fatal("provider challenge exposed")
	}
}
func TestSendPersistedRIDNoRetryOrTextRewrite(t *testing.T) {
	var calls atomic.Int32
	c, _ := newRPCFixture(t, func(method string, input object, anonymous bool) object {
		calls.Add(1)
		if method != "sendMessage" || input.str("rnd") != "9007199254740993" || input.str("text") != "  ب\\n  " {
			t.Errorf("wrong send projection: %v", input)
		}
		return nil
	})
	_, e := c.Send(context.Background(), domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "u0peer"}, Text: "  ب\\n  ", RequestID: "9007199254740993"})
	var de *domains.Error
	if !errors.As(e, &de) || !de.Ambiguous || calls.Load() != 1 {
		t.Fatalf("must remain unknown once: %v %d", e, calls.Load())
	}
}
func TestRecoveryCommitFailureDoesNotAdvance(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, input object, anonymous bool) object {
		if method != "getChatsUpdates" || input.str("state") != "100" {
			t.Errorf("wrong state %s %v", method, input)
		}
		return object{"new_state": "101", "chats": []any{object{"object_guid": "u0peer", "last_message_id": "42"}}}
	})
	cp := checkpoint{Version: 1, Account: "u0self", State: "100"}
	raw := checkpointJSON(cp)
	next, e := c.recoverPage(context.Background(), raw, cp, func(_ context.Context, b domains.EventBatch) error {
		if len(b.Events) != 1 || len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != raw {
			t.Errorf("invalid batch %+v", b)
		}
		return errors.New("commit failed")
	})
	if e == nil || next.State != "100" || len(next.Pending) != 0 {
		t.Fatalf("checkpoint advanced on failure: %+v %v", next, e)
	}
}
func TestSocketAccountScopeAndEditIdentity(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
	key, _ := authKey(fixtureAuth)
	calls := 0
	sink := func(context.Context, domains.EventBatch) error { calls++; return nil }
	if e := c.acceptFrame(context.Background(), []byte(`{"status":"OK"}`), key, sink); e != nil || calls != 0 {
		t.Fatal("pong must not advance storage")
	}
	encrypted, _ := encrypt(object{"user_guid": "u0different", "message_updates": []any{}}, key)
	raw, _ := json.Marshal(object{"type": "messenger", "data_enc": encrypted})
	if e := c.acceptFrame(context.Background(), raw, key, sink); e == nil || calls != 0 {
		t.Fatal("foreign update accepted")
	}
	m := object{"action": "Edit", "object_guid": "u0peer", "message_id": "9007199254740993", "message": object{"time": json.Number("1700000000"), "text": "ب", "type": "Text"}}
	if _, e := c.projectMessage("u0peer", m); e == nil {
		t.Fatal("undated edit received invented identity")
	}
	m["timestamp"] = "provider-revision"
	event, e := c.projectMessage("u0peer", m)
	if e != nil || event.Message.ID != "9007199254740993" || event.Direction != "unknown" {
		t.Fatalf("projection: %+v %v", event, e)
	}
}
func FuzzEncryptedResponse(f *testing.F) {
	f.Add("invalid")
	f.Add("aBBMfnsfLivZM3qCein+4V0sJSn4iCw0YFAt3kGVQOUK8D6R06A9TgmZ1CcCiZAz")
	f.Fuzz(func(t *testing.T, s string) { key, _ := authKey(fixtureAuth); _, _ = decrypt(s, key) })
}

func TestConnectSetupFailureDoesNotLeaveConnecting(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		if method != "getUserInfo" {
			t.Error(method)
		}
		return object{"user": object{"user_guid": "u0self"}}
	})
	raw, _ := json.Marshal(c.session)
	session := &domains.Session{Provider: domains.ProviderRubika, Version: 1, UserID: "u0self", Data: raw}
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) {
		return "", domains.E("AUTH_REQUIRED", "synthetic authentication failure", 401)
	}
	e := c.ConnectBatch(context.Background(), session, func(context.Context, domains.EventBatch) error { return nil })
	if e == nil || c.Status().Transport != "disconnected" || c.Status().Auth != "auth_required" {
		t.Fatalf("stale setup status: %+v %v", c.Status(), e)
	}
	for i := 0; i < 100; i++ {
		delay := pollJitter(60 * time.Second)
		if delay < 48*time.Second || delay > 60*time.Second {
			t.Fatal("poll delay escaped bounds")
		}
	}
}

func TestMalformedSuccessfulMutationReplyRemainsUnknown(t *testing.T) {
	for _, reply := range []object{{"status": "OK"}, {"data": object{}}, {"status": "unexpected", "status_det": "private-provider-error"}} {
		c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
		key, _ := authKey(fixtureAuth)
		encrypted, _ := encrypt(reply, key)
		body, _ := json.Marshal(object{"data_enc": encrypted})
		var calls atomic.Int32
		c.http.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})
		_, e := c.invoke(context.Background(), "sendMessage", object{}, true, "")
		var de *domains.Error
		if !errors.As(e, &de) || !de.Ambiguous || calls.Load() != 1 || strings.Contains(e.Error(), "private-provider-error") {
			t.Fatalf("malformed write response classification %v", e)
		}
	}
}
func TestPeerCheckpointCannotBeRebound(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { t.Fatal("wrong peer checkpoint contacted provider"); return nil })
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) {
		return checkpointJSON(checkpoint{Version: 1, Account: "u0self", Peer: "u0different", State: "100"}), nil
	}
	_, e := c.recoverPeer(context.Background(), pendingChat{GUID: "u0peer", LastMessage: "42"}, func(context.Context, domains.EventBatch) error { t.Fatal("wrong checkpoint committed"); return nil })
	if e == nil {
		t.Fatal("peer checkpoint rebound")
	}
}
