package rubikameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestChallengeDeliveryUsesReturnedTypeWithoutGuessingRequestedSMS(t *testing.T) {
	for _, tt := range []struct{ wire, want string }{{"SMS", "sms"}, {"Internal", "app"}, {"CallCode", "call"}, {"private-new-type", "unknown"}, {"", "unknown"}} {
		t.Run(tt.want+tt.wire, func(t *testing.T) {
			c, _ := newRPCFixture(t, func(method string, p object, anonymous bool) object {
				if method != "sendCode" || !anonymous || p.str("send_type") != "SMS" {
					t.Fatal("unexpected auth wire")
				}
				return object{"status": "OK", "phone_code_hash": "private-hash", "send_type": tt.wire, "send_code_timeout": 45}
			})
			ch, err := c.StartAuth(context.Background(), "10000000000")
			if err != nil {
				t.Fatal(err)
			}
			if ch.Delivery != tt.want || ch.NextDelivery != "" {
				t.Fatalf("delivery %#v", ch)
			}
			if tt.want == "unknown" && len(ch.AvailableDeliveries) != 0 || tt.want != "unknown" && (len(ch.AvailableDeliveries) != 1 || ch.AvailableDeliveries[0] != tt.want) {
				t.Fatal("delivery options were inferred")
			}
			if len(ch.AvailableDeliveries) > 0 {
				ch.AvailableDeliveries[0] = "tampered"
			}
			current := c.CurrentChallenge()
			public, _ := json.Marshal(current)
			if strings.Contains(string(public), "private-") || strings.Contains(string(public), "10000000000") || strings.Contains(string(public), "tampered") {
				t.Fatalf("public challenge leaked or aliased metadata %s", public)
			}
		})
	}
}
func TestPasswordFirstDoesNotClaimOTPSentUntilProviderAcknowledges(t *testing.T) {
	c, _ := newRPCFixture(t, func(_ string, p object, _ bool) object {
		if p.str("pass_key") == "" {
			return object{"status": "SendPassKey"}
		}
		return object{"status": "OK", "phone_code_hash": "private-hash", "send_type": "Internal"}
	})
	ch, err := c.StartAuth(context.Background(), "10000000000")
	if err != nil {
		t.Fatal(err)
	}
	if ch.State != "awaiting_password" || ch.Delivery != "" || len(ch.AvailableDeliveries) != 0 {
		t.Fatalf("claimed unsent delivery %#v", ch)
	}
	_, err = c.SubmitPassword(context.Background(), ch.ID, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	ch = c.CurrentChallenge()
	if ch.State != "awaiting_code" || ch.Delivery != "app" || len(ch.AvailableDeliveries) != 1 {
		t.Fatalf("password continuation lost delivery %#v", ch)
	}
}
