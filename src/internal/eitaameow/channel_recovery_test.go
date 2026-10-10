package eitaameow

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func TestChannelEmptyPageCommitFailureRetainsScopedCursor(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method != "updates.getChannelDifference" || p.num("pts") != 7 || asObject(p["channel"]).num("channel_id") != 91 {
			t.Errorf("wrong scoped request %s %#v", method, p)
		}
		return object{"_": "updates.channelDifferenceEmpty", "pts": 9, "final": true}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	before := channelCheckpointJSON(channelCheckpoint{Version: 1, Account: "42", Channel: "91", PTS: 7})
	c.cfg.LoadCheckpoint = func(_ context.Context, scope string) (string, error) {
		if scope != "eitaa.channel.91" {
			t.Error("scope mismatch")
		}
		return before, nil
	}
	failure := errors.New("synthetic commit failure")
	calls := 0
	ref := object{"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(2)}
	for range 2 {
		err := c.pollChannelPage(context.Background(), "91", ref, func(_ context.Context, b domains.EventBatch) error {
			calls++
			if len(b.Events) != 0 || len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != before {
				t.Errorf("non-atomic empty page %#v", b)
			}
			var cp channelCheckpoint
			_ = json.Unmarshal([]byte(b.Checkpoints[0].Next), &cp)
			if cp.PTS != 9 || cp.Account != "42" || cp.Channel != "91" {
				t.Errorf("wrong cursor %#v", cp)
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("commit failure ignored: %v", err)
		}
	}
	if calls != 2 {
		t.Fatal("page was not retried from durable state")
	}
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) {
		return channelCheckpointJSON(channelCheckpoint{Version: 1, Account: "another", Channel: "91", PTS: 7}), nil
	}
	if err := c.pollChannelPage(context.Background(), "91", ref, func(context.Context, domains.EventBatch) error { t.Fatal("mismatched account accepted"); return nil }); err == nil {
		t.Fatal("cross-account checkpoint accepted")
	}
}
func TestChannelDuplicateMessagesRetainStableIdentityAndAdvancePage(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls++
		return object{"_": "updates.channelDifference", "pts": 8 + calls, "final": true, "new_messages": []object{{"_": "message", "id": 12, "peer_id": object{"_": "peerChannel", "channel_id": int64(91)}, "date": 100, "message": "synthetic"}}, "other_updates": []object{}, "chats": []object{}, "users": []object{}}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	raw := channelCheckpointJSON(channelCheckpoint{Version: 1, Account: "42", Channel: "91", PTS: 7})
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return raw, nil }
	first := ""
	for range 2 {
		err := c.pollChannelPage(context.Background(), "91", object{"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(2)}, func(_ context.Context, b domains.EventBatch) error {
			if len(b.Events) != 1 || len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != raw {
				t.Fatalf("bad page %#v", b)
			}
			if b.Events[0].MediaRevision != nil {
				t.Fatal("diff page PTS assigned to an unversioned message")
			}
			if first == "" {
				first = b.Events[0].ID
			} else if first != b.Events[0].ID {
				t.Fatal("duplicate event identity changed")
			}
			raw = b.Checkpoints[0].Next
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestRecoveryLifetimeSurvivesConnectDeadlineAndDisconnectJoins(t *testing.T) {
	var calls atomic.Int32
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method != "updates.getDifference" {
			t.Errorf("unexpected method %s", method)
		}
		calls.Add(1)
		return object{"_": "updates.differenceEmpty", "date": 100, "seq": 0}, 200
	})
	c.cfg.PollInterval = 5 * time.Millisecond
	cp := checkpointJSON(checkpoint{Version: 1, Account: "42", PTS: 0, QTS: 0, Date: 100, Seq: 0})
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return cp, nil }
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	raw, _ := json.Marshal(c.session)
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.ConnectBatch(ctx, &domains.Session{Provider: domains.ProviderEitaa, Version: 1, UserID: "42", Data: raw}, func(context.Context, domains.EventBatch) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 2 {
		t.Fatal("connect request cancellation stopped accepted recovery lifetime")
	}
	if err := c.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopped := calls.Load()
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != stopped {
		t.Fatal("Disconnect failed to join recovery")
	}
}
func TestSessionRenewalCommitsBeforeActivationAndNeverRepeatsWrite(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[write], func(t *testing.T) {
			requests, rotations := 0, 0
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				if method == "eitaaRefreshToken" {
					rotations++
					if asObject(p["app_info"]).str("_") != "eitaaAppInfo" {
						t.Error("missing native app info")
					}
					return object{"_": "eitaa_updates_token", "token": "synthetic-new", "expire": 300, "date": 100}, 200
				}
				requests++
				if requests == 1 {
					return object{"_": "eitta_error", "code": 401, "text": "INVALID_LOGIN"}, 200
				}
				return object{"_": "updates.state", "pts": 1, "qts": 0, "date": 100, "seq": 0, "unread_count": 0}, 200
			})
			c.session.Token = "synthetic-old"
			c.session.UserID = "42"
			persisted := false
			c.SetSessionPersister(func(_ context.Context, s *domains.Session) error {
				if c.session.Token != "synthetic-old" {
					t.Error("activated before durable persistence")
				}
				p, e := decodeSession(s)
				if e != nil || p.Token != "synthetic-new" {
					t.Errorf("wrong envelope %v", e)
				}
				persisted = true
				return nil
			})
			method := "updates.getState"
			params := object{}
			if write {
				method = "messages.sendMessage"
				params = object{"peer": object{"_": "inputPeerSelf"}, "message": "synthetic", "random_id": int64(77)}
			}
			_, err := c.invoke(context.Background(), method, params, write, false)
			if !persisted || rotations != 1 || c.session.Token != "synthetic-new" {
				t.Fatal("rotation not committed")
			}
			if write {
				var de *domains.Error
				if !errors.As(err, &de) || !de.Ambiguous || requests != 1 {
					t.Fatalf("uncertain write repeated or misclassified %d %v", requests, err)
				}
			} else if err != nil || requests != 2 {
				t.Fatalf("read not resumed %d %v", requests, err)
			}
		})
	}
}
func TestSessionRenewalPersistenceFailureKeepsOldToken(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method == "eitaaRefreshToken" {
			return object{"_": "eitaa_updates_token", "token": "synthetic-new", "expire": 300, "date": 100}, 200
		}
		return object{"_": "eitaa_updates_expire_token"}, 200
	})
	c.session.Token = "synthetic-old"
	c.session.UserID = "42"
	failure := errors.New("synthetic commit failure")
	c.SetSessionPersister(func(context.Context, *domains.Session) error { return failure })
	_, err := c.invoke(context.Background(), "updates.getState", object{}, false, false)
	if !errors.Is(err, failure) || c.session.Token != "synthetic-old" {
		t.Fatalf("failed rotation changed active session: %v", err)
	}
}

func TestRecoverySetupFailureClearsConnectingStatusAndPollDelayIsBounded(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { return nil, 500 })
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	raw, _ := json.Marshal(c.session)
	failure := errors.New("synthetic storage failure")
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return "", failure }
	err := c.ConnectBatch(context.Background(), &domains.Session{Provider: domains.ProviderEitaa, Version: 1, UserID: "42", Data: raw}, func(context.Context, domains.EventBatch) error { return nil })
	if !errors.Is(err, failure) || c.Status().Transport != "disconnected" || c.Status().Recovery != "degraded" {
		t.Fatalf("stuck connecting: %v %#v", err, c.Status())
	}
	different := map[time.Duration]bool{}
	for range 100 {
		d := pollDelay(time.Second)
		if d < 800*time.Millisecond || d > 1200*time.Millisecond {
			t.Fatalf("unbounded jitter %v", d)
		}
		different[d] = true
		if d = pollDelay(10 * time.Minute); d < 40*time.Second || d > 60*time.Second {
			t.Fatalf("unbounded backoff %v", d)
		}
	}
	if len(different) < 2 {
		t.Fatal("polling lacks jitter")
	}
}

func TestChannelDifferenceCannotAcceptClassicGroupWithEqualNumericID(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		return object{"_": "updates.channelDifference", "pts": 9, "final": true,
			"new_messages":  []object{{"_": "message", "id": 12, "peer_id": object{"_": "peerChat", "chat_id": int64(91)}, "date": 100, "message": "synthetic"}},
			"other_updates": []object{}, "chats": []object{}, "users": []object{}}, 200
	})
	c.session.Token = "synthetic"
	c.session.UserID = "42"
	c.session.Peers = map[string]object{
		"group:91":         {"_": "inputPeerChat", "chat_id": int64(91)},
		"group:channel_91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(2)},
	}
	raw := channelCheckpointJSON(channelCheckpoint{Version: 1, Account: "42", Channel: "91", PTS: 7})
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return raw, nil }
	e := c.pollChannelPage(context.Background(), "91", c.session.Peers["group:channel_91"], func(context.Context, domains.EventBatch) error {
		t.Fatal("classic group event accepted in channel page")
		return nil
	})
	if e == nil {
		t.Fatal("malformed channel page accepted")
	}
}
