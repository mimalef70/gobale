package eitaameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestEditEventIdentityUsesActualUpdatePTS(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(map[bool]string{false: "account", true: "channel"}[channel], func(t *testing.T) {
			pts := int64(8)
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				peer := object{"_": "peerUser", "user_id": int64(43)}
				variant := "updateEditMessage"
				if channel {
					peer = object{"_": "peerChannel", "channel_id": int64(91)}
					variant = "updateEditChannelMessage"
				}
				edit := object{"_": variant, "message": object{"_": "message", "id": 12, "peer_id": peer, "date": 100, "edit_date": 101, "message": "Same caption"}, "pts": pts, "pts_count": 1}
				if channel {
					return object{"_": "updates.channelDifference", "pts": pts, "final": true, "new_messages": []object{}, "other_updates": []object{edit}, "chats": []object{}, "users": []object{}}, 200
				}
				return object{"_": "updates.difference", "new_messages": []object{}, "new_encrypted_messages": []object{}, "other_updates": []object{edit}, "chats": []object{}, "users": []object{}, "state": object{"_": "updates.state", "pts": pts, "qts": 0, "date": 101, "seq": 0, "unread_count": 0}}, 200
			})
			c.session.Token = "synthetic"
			c.session.UserID = "42"
			raw := channelCheckpointJSON(channelCheckpoint{Version: 1, Account: "42", Channel: "91", PTS: 7})
			c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return raw, nil }
			ids := []string{}
			sink := func(_ context.Context, b domains.EventBatch) error {
				if len(b.Events) != 1 {
					t.Fatalf("events=%d", len(b.Events))
				}
				scope := "account"
				if channel {
					scope = "channel:91"
				}
				revision := b.Events[0].MediaRevision
				if revision == nil || revision.Scope != scope || revision.Sequence != pts {
					t.Fatalf("wrong private revision: %#v", revision)
				}
				public, _ := json.Marshal(b.Events[0])
				if strings.Contains(string(public), "MediaRevision") || strings.Contains(string(public), "media_revision") || strings.Contains(string(public), "Sequence") {
					t.Fatalf("private revision leaked: %s", public)
				}
				ids = append(ids, b.Events[0].ID)
				return nil
			}
			for _, value := range []int64{8, 9, 8} {
				pts = value
				var err error
				if channel {
					err = c.pollChannelPage(context.Background(), "91", object{"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(2)}, sink)
				} else {
					cp := checkpoint{Version: 1, Account: "42", PTS: 7, Date: 100}
					_, err = c.pollPage(context.Background(), checkpointJSON(cp), cp, sink)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if ids[0] == ids[1] || ids[0] != ids[2] {
				t.Fatalf("edit identity lost provenance or retry stability: %v", ids)
			}
		})
	}
}

func TestOnlyActualMessageWrapperPTSProducesMediaRevision(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { return nil, 500 })
	message := object{"_": "message", "id": 12, "peer_id": object{"_": "peerUser", "user_id": int64(43)}, "date": 100, "message": "Synthetic"}
	original, e := c.projectMessage(message, "message")
	if e != nil {
		t.Fatal(e)
	}
	if original.MediaRevision != nil {
		t.Fatal("bare message invented revision")
	}
	for _, sequence := range []int64{0, 8} {
		event := original
		e = applyMessageUpdateRevision(&event, object{"_": "updateNewMessage", "message": message, "pts": sequence})
		if e != nil {
			t.Fatal(e)
		}
		if sequence == 0 && event.MediaRevision != nil {
			t.Fatal("zero/unavailable PTS became order evidence")
		}
		if sequence == 8 && (event.MediaRevision == nil || event.MediaRevision.Scope != "account" || event.MediaRevision.Sequence != 8) {
			t.Fatal("actual wrapper PTS lost")
		}
	}
	event := original
	if e = applyMessageUpdateRevision(&event, object{"_": "updateNewChannelMessage", "message": message, "pts": 8}); e == nil {
		t.Fatal("channel revision applied to different peer namespace")
	}
}
