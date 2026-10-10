package eitaameow

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestLiteralTLRequests(t *testing.T) {
	c, err := bundledCodec()
	if err != nil {
		t.Fatal(err)
	}
	// auth.logOut#5717da40 and inputPeerSelf#7da07ec9 are independent
	// literal wire assertions, not encoder/decoder round-trip assertions.
	got, err := c.encodeMethod("auth.logOut", object{})
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != "40da1757" {
		t.Fatalf("logout wire: %x", got)
	}
	var b bytes.Buffer
	if err := c.encodeType(&b, "InputPeer", object{"_": "inputPeerSelf"}, 0); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(b.Bytes()) != "c97ea07d" {
		t.Fatalf("self peer wire: %x", b.Bytes())
	}
	// A UTF-8 string is preserved without normalization/unescaping.
	b.Reset()
	if err := writeBytes(&b, []byte("\u0628")); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(b.Bytes()) != "02d8a800" {
		t.Fatalf("string wire: %x", b.Bytes())
	}
}

func TestLiteralTLStateAndBounds(t *testing.T) {
	c, err := bundledCodec()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString("3e2a6ca505000000020000000a0000000300000000000000")
	v, err := c.decodeResponse("updates.getState", raw)
	if err != nil {
		t.Fatal(err)
	}
	obj := v.(object)
	if obj["pts"] != int64(5) || obj["seq"] != int64(3) {
		t.Fatalf("bad state: %#v", obj)
	}
	if _, err = c.decodeResponse("updates.getState", append(raw, 0)); err == nil {
		t.Fatal("accepted trailing bytes")
	}
	for n := 0; n < len(raw); n++ {
		if _, err = c.decodeResponse("updates.getState", raw[:n]); err == nil {
			t.Fatalf("accepted truncation at %d", n)
		}
	}
	for _, bad := range [][]byte{{255, 0, 0, 0}, {254, 1, 0, 0, 0, 0, 0, 0}, {1, 0xff, 0, 0}, {1, 'a', 1, 0}} {
		if _, err = c.decode(bad, "string"); err == nil {
			t.Fatalf("accepted malformed string %x", bad)
		}
	}
	vector, _ := hex.DecodeString("15c4b51cffffffff")
	if _, err = c.decode(vector, "Vector<long>"); err == nil {
		t.Fatal("accepted unbounded vector")
	}
	if _, err = c.encodeMethod("auth.sendCode", object{"phone_number": "1", "api_id": int64(1) << 40, "api_hash": "test", "settings": object{"_": "codeSettings"}}); err == nil {
		t.Fatal("accepted int overflow")
	}
}

func FuzzTLDecoder(f *testing.F) {
	c, err := bundledCodec()
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte{0xb5, 0x75, 0x72, 0x99})
	f.Add([]byte{0x3e, 0x2a, 0x1c, 0xa5})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			t.Skip()
		}
		_, _ = c.decode(b, "Object")
	})
}
