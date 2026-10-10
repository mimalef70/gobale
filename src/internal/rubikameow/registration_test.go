package rubikameow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mimalef70/goomni/src/domains"
	"testing"
)

func TestRegistrationPreservesCredentialsAndChecksBeforeRetry(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "acknowledged", true: "interrupted response"}[interrupted], func(t *testing.T) {
			registered := false
			registrations := 0
			c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
				switch method {
				case "getUserInfo":
					if !registered {
						return object{"_fixture_error": object{"status": "ERROR_GENERIC", "status_det": "NOT_REGISTERED"}}
					}
					return object{"user": object{"user_guid": "u0self"}}
				case "registerDevice":
					registrations++
					registered = true
					if input.str("token_type") != "Web" || input.str("device_model") != "GoOmni" || len(input.str("device_hash")) != 64 {
						t.Error("registration identity missing")
					}
					if interrupted {
						return nil
					}
					return object{}
				default:
					t.Errorf("unexpected method %s", method)
					return nil
				}
			})
			raw, _ := json.Marshal(c.session)
			session := &domains.Session{Provider: domains.ProviderRubika, Version: 1, UserID: "u0self", Data: raw}
			stop := errors.New("stop before socket")
			c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return "", stop }
			sink := func(context.Context, domains.EventBatch) error { return nil }
			err := c.ConnectBatch(context.Background(), session, sink)
			if err == nil || c.Status().Auth == "auth_required" {
				t.Fatalf("registration discarded auth: %v %+v", err, c.Status())
			}
			if interrupted {
				err = c.ConnectBatch(context.Background(), session, sink)
			}
			if !errors.Is(err, stop) || registrations != 1 {
				t.Fatalf("registration repeated or profile not verified: %d %v", registrations, err)
			}
		})
	}
}
