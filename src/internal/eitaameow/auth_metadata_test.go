package eitaameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestChallengeDeliveryUsesProviderConstructorsAndSurvivesPassword(t *testing.T) {
	for _, tt := range []struct{ constructor, want string }{
		{"auth.sentCodeTypeSms", "sms"}, {"auth.sentCodeTypeApp", "app"}, {"auth.sentCodeTypeCall", "call"}, {"auth.sentCodeTypeFlashCall", "flash_call"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			c, _ := nativeFixture(t, func(method string, _ object) (object, int) {
				if method == "auth.signIn" {
					return object{"_": "error", "code": 401, "text": "SESSION_PASSWORD_NEEDED"}, 200
				}
				if method != "auth.sendCode" {
					t.Fatal(method)
				}
				kind := object{"_": tt.constructor, "length": 5, "pattern": "private-pattern"}
				return object{"_": "auth.sentCode", "type": kind, "next_type": object{"_": "auth.codeTypeCall"}, "phone_code_hash": "private-hash", "timeout": 45}, 200
			})
			ch, err := c.StartAuth(context.Background(), "10000000000")
			if err != nil {
				t.Fatal(err)
			}
			if ch.Delivery != tt.want || ch.NextDelivery != "call" || ch.ResendAfterSeconds == nil || *ch.ResendAfterSeconds != 45 {
				t.Fatalf("challenge %#v", ch)
			}
			if len(ch.AvailableDeliveries) > 2 || len(ch.AvailableDeliveries) < 1 {
				t.Fatal("inferred delivery options")
			}
			ch.AvailableDeliveries[0] = "tampered"
			*ch.ResendAfterSeconds = 1
			_, _ = c.SubmitCode(context.Background(), ch.ID, "12345")
			current := c.CurrentChallenge()
			if current.State != "awaiting_password" || current.Delivery != tt.want || current.NextDelivery != "call" || current.AvailableDeliveries[0] == "tampered" || *current.ResendAfterSeconds != 45 {
				t.Fatalf("metadata lost or rebound %#v", current)
			}
			public, _ := json.Marshal(current)
			for _, secret := range []string{"private-hash", "private-pattern", "12345", "10000000000"} {
				if strings.Contains(string(public), secret) {
					t.Fatal("private authentication metadata escaped")
				}
			}
		})
	}
	if codeDelivery("auth.unreviewedSecret") != "unknown" || codeDelivery("") != "unknown" {
		t.Fatal("unknown delivery was guessed")
	}
}
