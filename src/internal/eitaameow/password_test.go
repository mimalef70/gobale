package eitaameow

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func TestPassword2IndependentHashAndLiteralWire(t *testing.T) {
	c, _ := bundledCodec()
	// Constructed independently with Python hashlib/struct, using UTF-8 and TL
	// length/padding rules; this does not round-trip the project's encoder.
	literal, _ := hex.DecodeString("47b439ca0000000010000102030405060708090a0b0c0d0e0f0000000000000000000000000000000000000000000000")
	state, err := c.decodeResponse("account.getPassword", literal)
	if err != nil {
		t.Fatal(err)
	}
	method, params, err := passwordCheck("synthetic-بله", asObject(state), authChallenge{Phone: "10000000000", Code: "12345"})
	if err != nil || method != "auth.checkPassword2" {
		t.Fatalf("password2 selection %s %v", method, err)
	}
	wire, err := c.encodeMethod(method, params)
	want := "1fda8b8920a77a88149b6b05c4ed930eff7098c36819723742c7ce7f8a0311c1f0b000e6540000000300000005313233343500000b3130303030303030303030"
	if err != nil || hex.EncodeToString(wire) != want {
		t.Fatalf("wire %x %v", wire, err)
	}
	if _, err = c.decodeResponse("users.getFullUser", literal); err == nil {
		t.Fatal("password2 accepted outside password query")
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write(literal)
	_ = gz.Close()
	var packed bytes.Buffer
	if err = c.encodeType(&packed, "Object", object{"_": "gzip_packed", "packed_data": compressed.Bytes()}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = c.decodeResponse("account.getPassword", packed.Bytes()); err != nil {
		t.Fatalf("bounded packed password2: %v", err)
	}
}

func TestNativePassword2LoginBindsOTPAndDoesNotPersistSecrets(t *testing.T) {
	ctx := context.Background()
	checks := 0
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		switch method {
		case "auth.sendCode":
			return object{"_": "auth.sentCode", "type": object{"_": "auth.sentCodeTypeSms", "length": 5}, "phone_code_hash": "private-hash"}, 200
		case "auth.signIn":
			return object{"_": "error", "code": 401, "text": "SESSION_PASSWORD_NEEDED"}, 200
		case "account.getPassword":
			return password2Fixture(), 200
		case "auth.checkPassword2":
			checks++
			if p.str("phone_number") != "10000000000" || p.str("phone_code") != "12345" {
				t.Fatal("password not bound to successful OTP challenge")
			}
			hash, _ := p["password_hash"].([]byte)
			if hex.EncodeToString(hash) != "a77a88149b6b05c4ed930eff7098c36819723742c7ce7f8a0311c1f0b000e654" {
				t.Fatalf("hash %x", hash)
			}
			if checks == 1 {
				return object{"_": "error", "code": 400, "text": "PASSWORD_HASH_INVALID"}, 200
			}
			return object{"_": "auth.authorization", "token": "synthetic-token", "user": object{"_": "user", "id": 42, "self": true}}, 200
		default:
			t.Fatalf("unreviewed auth RPC %s", method)
		}
		return nil, 500
	})
	ch, err := c.StartAuth(ctx, "+10000000000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.SubmitCode(ctx, ch.ID, "12345"); err == nil || c.Status().Auth != "awaiting_password" {
		t.Fatalf("password transition %v", err)
	}
	c.SetSessionPersister(func(context.Context, *domains.Session) error {
		t.Fatal("initial authorization must be persisted by service")
		return nil
	})
	if _, err = c.SubmitPassword(ctx, ch.ID, "synthetic-بله"); err == nil {
		t.Fatal("invalid password reported successful")
	}
	session, err := c.SubmitPassword(ctx, ch.ID, "synthetic-بله")
	if err != nil || session.UserID != "42" || checks != 2 {
		t.Fatalf("login %v %v", session, err)
	}
	raw, _ := json.Marshal(session)
	if strings.Contains(string(raw), "12345") || strings.Contains(string(session.Data), "12345") || strings.Contains(string(session.Data), "synthetic-بله") || c.challenge.Code != "" {
		t.Fatal("short-lived password/OTP survived successful auth")
	}
}
func password2Fixture() object {
	return object{"_": "account.password2", "current_salt": []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, "new_salt": []byte{}, "new_secure_salt": []byte{}, "secure_random": []byte{}, "hint": "", "email_unconfirmed_pattern": ""}
}

func TestPasswordVariantsFailClosedWithoutOTPBinding(t *testing.T) {
	for _, state := range []object{{"_": "account.password2", "current_salt": []byte{}}, {"_": "account.password2", "current_salt": bytes.Repeat([]byte{1}, 257)}, {"_": "unknown.password", "current_salt": []byte{1}}, {"_": "account.password", "current_salt": []byte{1}}} {
		if _, _, err := passwordCheck("synthetic", state, authChallenge{Phone: "10000000000", Code: "12345"}); err == nil {
			t.Fatal("invalid or unknown password variant admitted")
		}
	}
	if _, _, err := passwordCheck("synthetic", password2Fixture(), authChallenge{Phone: "10000000000"}); err == nil {
		t.Fatal("password2 lost code binding")
	}
	if _, _, err := passwordCheck("\xff", password2Fixture(), authChallenge{Phone: "10000000000", Code: "12345"}); err == nil {
		t.Fatal("non-UTF8 password admitted")
	}
	calls := 0
	c, _ := nativeFixture(t, func(string, object) (object, int) { calls++; return nil, 500 })
	c.challenge = authChallenge{ID: "expired", Phone: "10000000000", Code: "12345", Password: true, Expires: time.Now().Add(-time.Second)}
	_, err := c.SubmitPassword(context.Background(), "expired", "synthetic")
	var de *domains.Error
	if !errors.As(err, &de) || de.Code != "CHALLENGE_EXPIRED" || calls != 0 {
		t.Fatalf("expired challenge called provider %d %v", calls, err)
	}
}
