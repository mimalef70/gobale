package eitaameow

import (
	"encoding/hex"
	"testing"
)

func TestLiteralAuthorizationPrivateExtension(t *testing.T) {
	c, _ := bundledCodec()
	// Independent literal: auth.authorization, flag 10, synthetic token, self
	// user 42, and the observed terminal TL bytes layout. No captured values.
	raw, _ := hex.DecodeString("160905cd000400000f73796e7468657469632d746f6b656ecb6dd2ec0004000000000000000000002a00000000000000387878787878787878787878787878787878787878787878787878787878787878787878787878787878787878787878787878787878787878000000")
	v, err := c.decodeResponse("auth.signIn", raw)
	if err != nil {
		t.Fatal(err)
	}
	o := asObject(v)
	if o.str("token") != "synthetic-token" || asObject(o["user"]).num("id") != 42 || len(o["private_extension"].([]byte)) != 56 {
		t.Fatal("authorization fields shifted")
	}
	if _, err = c.decodeResponse("auth.signIn", append(append([]byte(nil), raw...), 0)); err == nil {
		t.Fatal("extra bytes accepted")
	}
	for n := 48; n < len(raw); n++ {
		if _, err = c.decodeResponse("auth.signIn", raw[:n]); err == nil {
			t.Fatalf("truncated optional bytes accepted at %d", n)
		}
	}
	raw[5] = 0 // identical unflagged suffix must be rejected, not silently consumed.
	if _, err = c.decodeResponse("auth.signIn", raw); err == nil {
		t.Fatal("unflagged extension accepted")
	}
}
