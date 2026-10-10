package rubikameow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestBotServiceMessageLifecycleAndContract(t *testing.T) {
	for _, kind := range []string{"bot", "service"} {
		t.Run(kind, func(t *testing.T) {
			guid := map[string]string{"bot": "b0fixture", "service": "s0fixture"}[kind]
			peer := domains.Peer{Type: kind, ID: guid}
			contract := Contract{}
			if contract.ValidatePeer(peer) != nil || contract.ValidateUserID(guid) || !contract.ValidateSenderID(guid) {
				t.Fatal("account/actor namespaces conflated")
			}
			if contract.ValidatePeer(domains.Peer{Type: "user", ID: guid}) == nil {
				t.Fatal("bot/service reinterpreted as user")
			}
			c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
				switch method {
				case "getBotInfo", "getServiceInfo":
					return object{kind: object{kind + "_guid": guid, "title": "synthetic", "access_hash": "secret"}}
				case "getMessages":
					return object{"messages": []any{object{"message_id": "42", "time": 1700000000, "type": "Text", "text": "synthetic", "author_object_guid": guid}}, "has_continue": false}
				case "sendMessage":
					if input.str("object_guid") != guid || input.str("rnd") != "77" {
						t.Fatal("send scope changed")
					}
					return object{"status": "OK", "message_update": object{"object_guid": guid, "message_id": "43"}}
				default:
					t.Fatalf("unexpected method %s", method)
					return nil
				}
			})
			raw, _ := json.Marshal(object{"peer": peer})
			info, err := c.Call(context.Background(), "chat.info", raw)
			if err != nil {
				t.Fatal(err)
			}
			var infoFields map[string]any
			_ = json.Unmarshal(info, &infoFields)
			if infoFields[kind+"_guid"] != guid || infoFields["access_hash"] != nil {
				t.Fatal("unsafe info projection")
			}
			history, err := c.Call(context.Background(), "chat.history", raw)
			if err != nil {
				t.Fatal(err)
			}
			var h struct {
				Items []domains.Message `json:"items"`
			}
			_ = json.Unmarshal(history, &h)
			if len(h.Items) != 1 || h.Items[0].ChatID != guid || h.Items[0].IsFromMe == nil || *h.Items[0].IsFromMe {
				t.Fatal("history actor/peer not preserved")
			}
			for _, action := range []string{"New", "Edit", "Delete"} {
				event, err := c.projectMessage(guid, object{"object_guid": guid, "action": action, "timestamp": "revision1", "message_id": "42", "message": object{"time": 1700000000, "type": "Text", "text": "synthetic", "author_object_guid": guid}})
				if err != nil || event.Peer != peer {
					t.Fatalf("projection %s: %v", action, err)
				}
				if action != "Delete" && (event.Direction != "incoming" || event.SenderID != guid) {
					t.Fatal("actor provenance lost")
				}
			}
			result, err := c.Send(context.Background(), domains.SendRequest{Peer: peer, Text: "synthetic", RequestID: "77", ReplyMessageID: "42"})
			if err != nil || result.MessageID != "43" {
				t.Fatalf("send/reply: %v", err)
			}
			if _, _, err = contract.NormalizeOperation("group.info", raw); err == nil {
				t.Fatal("group operations accepted bot/service")
			}
		})
	}
}

func TestRecoveryGapProvenanceAndUnsupportedCoverage(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		if method == "getChatsUpdates" {
			return object{"new_state": "101", "chats": []any{object{"object_guid": "z0future"}}}
		}
		return object{"status": "OldState"}
	})
	cp := checkpoint{Version: 2, Account: "u0self", State: "100"}
	var accepted domains.EventBatch
	next, err := c.recoverPage(context.Background(), checkpointJSON(cp), cp, func(_ context.Context, b domains.EventBatch) error { accepted = b; return nil })
	if err != nil || next.Gap || len(accepted.Events) != 1 || accepted.Events[0].Type != "protocol.unsupported_update" || !c.Status().UnsupportedUpdatesObserved {
		t.Fatal("unsupported type became recovery gap")
	}
	c.setRecoveryStatus(next)
	if c.Status().Recovery == "gap_detected" {
		t.Fatal("false gap")
	}
	cp.Pending = []pendingChat{{GUID: "s0fixture", LastMessage: "42"}}
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) {
		return checkpointJSON(checkpoint{Version: 2, Account: "u0self", Peer: "s0fixture", State: "100"}), nil
	}
	next, err = c.recoverPage(context.Background(), checkpointJSON(cp), cp, func(context.Context, domains.EventBatch) error { return nil })
	if err != nil || !next.Gap || next.GapReason != "message_state_expired" {
		t.Fatalf("peer gap not propagated %+v %v", next, err)
	}
	c.setRecoveryStatus(next)
	c.setStatus("", "disconnected", "degraded", "CONNECTION_LOST")
	if c.Status().Recovery != "gap_detected" {
		t.Fatal("transport error erased proven gap")
	}
	old, err := parseCheckpoint(`{"version":1,"account":"u0self","state":"100","gap":true}`, "u0self")
	if err != nil || old.Version != 2 || !old.Gap || old.GapReason != "legacy_unclassified" {
		t.Fatal("legacy evidence lost")
	}
	c.setRecoveryStatus(old)
	if c.Status().Recovery == "gap_detected" || c.Status().RecoveryIssue != "legacy_unclassified" {
		t.Fatal("legacy ambiguity invented proof")
	}
}

func TestUnsupportedSocketAcceptanceIsAtomic(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
	key, _ := authKey(fixtureAuth)
	encrypted, _ := encrypt(object{"user_guid": "u0self", "message_updates": []any{object{"object_guid": "z0future"}}}, key)
	frame, _ := json.Marshal(object{"type": "messenger", "data_enc": encrypted})
	failure := errors.New("commit failed")
	if err := c.acceptFrame(context.Background(), frame, key, func(context.Context, domains.EventBatch) error { return failure }); !errors.Is(err, failure) || c.Status().UnsupportedUpdatesObserved {
		t.Fatal("unaccepted update changed coverage")
	}
	if err := c.acceptFrame(context.Background(), frame, key, func(context.Context, domains.EventBatch) error { return nil }); err != nil || !c.Status().UnsupportedUpdatesObserved {
		t.Fatal("accepted diagnostic missing")
	}
}

func TestUpgradeDiscoversPreviouslySkippedPeersWithoutSkippingAccountUpdates(t *testing.T) {
	cp, err := parseCheckpoint(`{"version":1,"account":"u0self","state":"100","gap":true}`, "u0self")
	if err != nil || !cp.RefreshPeers {
		t.Fatal("upgrade discovery missing")
	}
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		if method != "getChats" {
			t.Fatal("expected discovery listing")
		}
		return object{"state": "999", "chats": []any{object{"object_guid": "s0fixture", "last_message_id": "42"}}, "has_continue": false}
	})
	next, err := c.recoverPage(context.Background(), "old durable value", cp, func(_ context.Context, b domains.EventBatch) error {
		if b.Checkpoints[0].Expected != "old durable value" {
			t.Fatal("CAS changed")
		}
		return nil
	})
	if err != nil || next.State != "100" || next.RefreshPeers || len(next.Pending) != 1 || next.Pending[0].GUID != "s0fixture" || next.GapReason != "legacy_unclassified" {
		t.Fatalf("upgrade skipped account state %+v %v", next, err)
	}
}

func TestConnectFailurePreservesDurableGapProvenance(t *testing.T) {
	for _, reason := range []string{"message_state_expired", "legacy_unclassified"} {
		t.Run(reason, func(t *testing.T) {
			c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
				if method != "getUserInfo" {
					t.Fatal("unexpected RPC")
				}
				return object{"user": object{"user_guid": "u0self"}}
			})
			c.socket = "ws://127.0.0.1:0"
			c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) {
				return checkpointJSON(checkpoint{Version: 2, Account: "u0self", State: "100", Gap: true, GapReason: reason}), nil
			}
			raw, _ := json.Marshal(c.session)
			err := c.ConnectBatch(context.Background(), &domains.Session{Provider: domains.ProviderRubika, Version: 1, UserID: "u0self", Data: raw}, func(context.Context, domains.EventBatch) error {
				t.Fatal("unconnected client committed progress")
				return nil
			})
			status := c.Status()
			if err == nil || status.Transport != "disconnected" || status.RecoveryIssue != reason || status.LastError != "CONNECTION_FAILED" {
				t.Fatalf("failure hid evidence %+v %v", status, err)
			}
			if (status.Recovery == "gap_detected") != (reason == "message_state_expired") {
				t.Fatal("legacy/proven gap conflated")
			}
		})
	}
}
