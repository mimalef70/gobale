package eitaameow

import (
	"encoding/hex"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"math"
	"strings"
	"testing"
)

func TestStructuredContentProjectionAndPrivacy(t *testing.T) {
	c, e := New(Config{})
	if e != nil {
		t.Fatal(e)
	}
	c.session.UserID = "42"
	cases := []struct {
		kind  string
		media object
	}{
		{"contact", object{"_": "messageMediaContact", "phone_number": "+999000000000", "first_name": "Synthetic", "last_name": "Fixture", "vcard": "private-vcard-payload", "user_id": int64(9007199254740993)}},
		{"location", object{"_": "messageMediaGeo", "geo": object{"_": "geoPoint", "lat": float64(35.5), "long": float64(51.25), "access_hash": int64(777)}}},
		{"poll", object{"_": "messageMediaPoll", "poll": object{"_": "poll", "id": int64(9007199254740993), "question": "ب؟", "answers": []any{object{"_": "pollAnswer", "text": "A", "option": []byte{0}}, object{"_": "pollAnswer", "text": "B", "option": []byte{1}}}, "multiple_choice": true}, "results": object{"_": "pollResults", "total_voters": 2, "results": []any{object{"_": "pollAnswerVoters", "option": []byte{0}, "voters": 2, "chosen": true}}, "recent_voters": []int64{9007199254740993}}}},
	}
	for _, tt := range cases {
		t.Run(tt.kind, func(t *testing.T) {
			m := object{"_": "message", "id": 7, "peer_id": object{"_": "peerUser", "user_id": int64(43)}, "date": 100, "message": "", "media": tt.media}
			event, e := c.projectMessage(m, "message")
			if e != nil || event.Message.Kind != tt.kind || !event.Message.Supported {
				t.Fatalf("projection: %+v %v", event, e)
			}
			body := string(event.Payload)
			for _, secret := range []string{"access_hash", "private-vcard-payload", "recent_voters"} {
				if strings.Contains(body, secret) {
					t.Fatalf("private data in public content: %s", body)
				}
			}
			if tt.kind != "location" && !strings.Contains(body, `"9007199254740993"`) {
				t.Error("provider ID lost string precision")
			}
			var out map[string]any
			if json.Unmarshal(event.Payload, &out) != nil || out[tt.kind] == nil {
				t.Errorf("missing structure %s", event.Payload)
			}
		})
	}
}
func TestStructuredContentBoundsAndUnknownVariant(t *testing.T) {
	c, _ := New(Config{})
	base := func(media object) object {
		return object{"_": "message", "id": 7, "peer_id": object{"_": "peerUser", "user_id": int64(43)}, "date": 100, "message": "", "media": media}
	}
	for _, media := range []object{{"_": "messageMediaGeo", "geo": object{"_": "geoPoint", "lat": math.NaN(), "long": float64(1)}}, {"_": "messageMediaGeo", "geo": object{"_": "geoPoint", "lat": float64(91), "long": float64(1)}}, {"_": "messageMediaContact", "first_name": strings.Repeat("x", 1025)}, {"_": "messageMediaPoll", "poll": object{"_": "poll", "id": 1, "question": "q", "answers": []object{}}}} {
		if _, e := c.projectMessage(base(media), "message"); e == nil {
			t.Fatal("malformed content accepted")
		}
	}
	event, e := c.projectMessage(base(object{"_": "messageMediaUnsupported"}), "message")
	if e != nil || event.Message.Supported || event.Message.Kind != "unsupported" {
		t.Fatalf("unknown content asserted support: %+v %v", event, e)
	}
}

func TestObservedGeoPoint84LiteralProjection(t *testing.T) {
	// Independent legacy GeoPoint wire: constructor, longitude 51.25, latitude 35.5.
	raw, _ := hex.DecodeString("0cd749200000000000a049400000000000c04140")
	c, _ := bundledCodec()
	geo, err := c.decode(raw, "GeoPoint")
	if err != nil {
		t.Fatal(err)
	}
	msg := &domains.Message{}
	projected, err := structuredContent(object{"_": "messageMediaGeo", "geo": geo}, msg)
	if err != nil || msg.Kind != "location" || asObject(projected["location"])["latitude"] != float64(35.5) {
		t.Fatalf("geo projection %v %v", projected, err)
	}
}
