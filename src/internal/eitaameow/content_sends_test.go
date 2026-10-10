package eitaameow

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestContentSendLiteralMedia(t *testing.T) {
	c, err := bundledCodec()
	if err != nil {
		t.Fatal(err)
	}
	// Independent current Android geo constructor, LE doubles and TL strings.
	for _, tt := range []struct {
		media   object
		literal string
	}{
		{object{"_": "inputMediaGeoPoint", "geo_point": object{"_": "inputGeoPoint", "lat": 35.5, "long": 51.25}}, "4441c4f9c9acb7f30000000000c041400000000000a04940"},
		{object{"_": "inputMediaContact", "phone_number": "999000000000", "first_name": "A", "last_name": "B", "vcard": ""}, "fb7dabf80c393939303030303030303030000000014100000142000000000000"},
	} {
		var b bytes.Buffer
		if e := c.encodeType(&b, "InputMedia", tt.media, 0); e != nil {
			t.Fatal(e)
		}
		if hex.EncodeToString(b.Bytes()) != tt.literal {
			t.Fatalf("wire=%x", b.Bytes())
		}
	}
}

func TestLocationLiteralKeepsCaptionAndNonceAfterNonzeroCoordinates(t *testing.T) {
	c, err := bundledCodec()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.encodeMethod("messages.sendMedia", object{"peer": object{"_": "inputPeerSelf"}, "media": object{"_": "inputMediaGeoPoint", "geo_point": object{"_": "inputGeoPoint", "lat": 1.25, "long": 2.5}}, "message": "", "random_id": int64(77)})
	if err != nil {
		t.Fatal(err)
	}
	// Full request, independently laid out: method/flags/peer/media/geo, two
	// LE doubles, empty TL string, then the durable nonce. A zero-only fixture
	// would hide the server's observed coordinate/nonce alignment defect.
	want := "a9eb913400000000c97ea07d4441c4f9c9acb7f3000000000000f43f0000000000000440000000004d00000000000000"
	if hex.EncodeToString(raw) != want {
		t.Fatalf("location request literal mismatch: %x", raw)
	}
	web, _ := hex.DecodeString("af2f224800000000000000000000f43f0000000000000440")
	if _, err = c.decode(web, "InputGeoPoint"); err == nil {
		t.Fatal("unreviewed Web geo form retained as a fallback")
	}
}

func TestContentSendDurableNonceWireAndPublicResult(t *testing.T) {
	for _, name := range []string{"send.location", "send.contact"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls++
				if method != "messages.sendMedia" || p.num("random_id") != 77 || asObject(p["peer"]).str("_") != "inputPeerSelf" || p.str("message") != "" {
					t.Errorf("bad send %s %#v", method, p)
				}
				media := asObject(p["media"])
				if name == "send.location" {
					geo := asObject(media["geo_point"])
					if media.str("_") != "inputMediaGeoPoint" || geo["lat"] != float64(35.5) || geo["long"] != float64(51.25) {
						t.Errorf("bad geo %#v", media)
					}
				} else if media.str("_") != "inputMediaContact" || media.str("phone_number") != "999000000000" || media.str("first_name") != "ب" || media.str("last_name") != "Fixture" || media.str("vcard") != "" {
					t.Errorf("bad contact %#v", media)
				}
				return object{"_": "updateShortSentMessage", "id": 12, "pts": 8, "pts_count": 1, "date": 100}, 200
			})
			c.session.UserID = "42"
			c.session.Token = "synthetic"
			body := `{"peer":{"type":"user","id":"42"},"request_id":"77","latitude":35.5,"longitude":51.25}`
			if name == "send.contact" {
				body = `{"peer":{"type":"user","id":"42"},"request_id":"77","phone":"999000000000","first_name":"ب","last_name":"Fixture"}`
			}
			out, e := c.Call(context.Background(), name, json.RawMessage(body))
			if e != nil || calls != 1 || !strings.Contains(string(out), `"message_id":"12"`) {
				t.Fatalf("result=%s calls=%d error=%v", out, calls, e)
			}
			if strings.Contains(string(out), "phone") || strings.Contains(string(out), "access_hash") {
				t.Fatalf("private send content in result: %s", out)
			}
			found := false
			for _, op := range (Contract{}).Operations() {
				if op.Operation == name {
					found = op.Schedulable
				}
			}
			if !found {
				t.Fatal("content send is not schedulable")
			}
		})
	}
}

func TestContentSendAdmissionPrecedesWire(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("invalid content contacted provider"); return nil, 500 })
	c.session.UserID = "42"
	c.session.Token = "synthetic"
	for _, tt := range []struct{ name, body string }{
		{"send.location", `{"latitude":91,"longitude":0}`},
		{"send.location", `{"latitude":0,"longitude":181}`},
		{"send.location", `{"latitude":0}`},
		{"send.location", `{"latitude":null,"longitude":0}`},
		{"send.location", `{"latitude":1e999,"longitude":0}`},
		{"send.location", `{"latitude":0,"longitude":0,"live_period":30}`},
		{"send.contact", `{"phone":"invalid","first_name":"A"}`},
		{"send.contact", `{"phone":"999000000000","first_name":" "}`},
		{"send.contact", `{"phone":"999000000000","first_name":"A","vcard":"private"}`},
		{"send.contact", `{"phone":"999000000000","first_name":"A","user_id":"43"}`},
	} {
		body := `{"peer":{"type":"user","id":"42"},"request_id":"77",` + tt.body[1:]
		if _, e := c.Call(context.Background(), tt.name, json.RawMessage(body)); e == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, name := range []string{"send.location", "send.contact"} {
		body := `{"peer":{"type":"user","id":"42"},"latitude":0,"longitude":0}`
		if name == "send.contact" {
			body = `{"peer":{"type":"user","id":"42"},"phone":"999000000000","first_name":"A"}`
		}
		if _, e := c.Call(context.Background(), name, json.RawMessage(body)); e == nil {
			t.Fatal("send admitted without persisted RID")
		}
	}
}

func TestContentSendUncertainResponseNeverRetries(t *testing.T) {
	for _, name := range []string{"send.contact", "send.location"} {
		for _, lost := range []bool{false, true} {
			calls := 0
			c, _ := nativeFixture(t, func(string, object) (object, int) {
				calls++
				if lost {
					return nil, 500
				}
				return object{"_": "updates", "updates": []object{}, "users": []object{}, "chats": []object{}, "date": 100, "seq": 1}, 200
			})
			c.session.UserID = "42"
			c.session.Token = "synthetic"
			body := `{"peer":{"type":"user","id":"42"},"request_id":"77","latitude":0,"longitude":0}`
			if name == "send.contact" {
				body = `{"peer":{"type":"user","id":"42"},"request_id":"77","phone":"999000000000","first_name":"A"}`
			}
			_, e := c.Call(context.Background(), name, json.RawMessage(body))
			var de *domains.Error
			if !errors.As(e, &de) || !de.Ambiguous || calls != 1 {
				t.Fatalf("%s uncertain send calls=%d error=%v", name, calls, e)
			}
		}
	}
}

func TestLocationZeroOrForeignResponseNonceCannotProveAcceptance(t *testing.T) {
	for _, nonce := range []int64{0, 78} {
		calls := 0
		c, _ := nativeFixture(t, func(string, object) (object, int) {
			calls++
			return object{"_": "updates", "updates": []object{{"_": "updateMessageID", "random_id": nonce, "id": 12}}, "users": []object{}, "chats": []object{}, "date": 100, "seq": 1}, 200
		})
		c.session.UserID, c.session.Token = "42", "synthetic"
		_, err := c.Call(context.Background(), "send.location", json.RawMessage(`{"peer":{"type":"user","id":"42"},"request_id":"77","latitude":0,"longitude":0}`))
		var de *domains.Error
		if !errors.As(err, &de) || !de.Ambiguous || calls != 1 {
			t.Fatalf("unrelated nonce accepted or retried: %v calls=%d", err, calls)
		}
	}
}
