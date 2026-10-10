package rubikameow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestReceivedContactUsesObservedReadLayout(t *testing.T) {
	// The response layout differs from sendMessage's message_contact argument.
	var record object
	if err := json.Unmarshal([]byte(`{"type":"ContactMessage","contact_message":{"first_name":"Synthetic","last_name":"Contact","phone_number":"989000000000","user_guid":"u0synthetic","access_hash":"private","raw":{"token":"private"}}}`), &record); err != nil {
		t.Fatal(err)
	}
	msg := &domains.Message{}
	out, handled, err := projectExtendedMessage(record, msg)
	b, _ := json.Marshal(out)
	if err != nil || !handled || msg.Kind != "contact" || !msg.Supported || !strings.Contains(string(b), `"phone_number":"989000000000"`) || strings.Contains(string(b), "private") {
		t.Fatalf("contact projection %s %#v %v", b, msg, err)
	}
}

func TestContactReadRejectsMalformedReviewedFields(t *testing.T) {
	for _, contact := range []any{"invalid", object{"first_name": 42}, object{"phone_number": strings.Repeat("x", 1025)}, object{"user_guid": "g0group"}, object{"user_guid": 42}} {
		if _, handled, err := projectExtendedMessage(object{"type": "ContactMessage", "contact_message": contact}, &domains.Message{}); !handled || err == nil {
			t.Fatal("malformed contact accepted")
		}
	}
}

func TestContactReadDoesNotGuessWriteLayout(t *testing.T) {
	msg := &domains.Message{}
	_, handled, err := projectExtendedMessage(object{"type": "ContactMessage", "message_contact": object{"first_name": "Synthetic"}}, msg)
	if err != nil || handled || msg.Supported || msg.Kind != "unsupported" {
		t.Fatal("unobserved receive layout accepted")
	}
}
