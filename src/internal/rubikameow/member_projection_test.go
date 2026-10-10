package rubikameow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMemberListsPreserveReviewedIdentityWithoutPrivateFields(t *testing.T) {
	for _, name := range []string{"group.members", "group.admins", "group.banned"} {
		t.Run(name, func(t *testing.T) {
			c, _ := newRPCFixture(t, func(method string, p object, _ bool) object {
				if p.str("group_guid") != "g0test" {
					t.Fatal("wrong group scope")
				}
				return object{"in_chat_members": []object{{"member_guid": "u0member", "member_type": "User", "first_name": "Synthetic", "access_hash": "private-hash", "avatar_thumbnail": object{"file_id": "private-file"}}}, "has_continue": false, "next_start_id": "next-page"}
			})
			raw, err := c.Call(context.Background(), name, json.RawMessage(`{"peer":{"type":"group","id":"g0test"}}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"member_guid":"u0member"`) || !strings.Contains(string(raw), `"member_type":"User"`) || !strings.Contains(string(raw), `"next_start_id":"next-page"`) || strings.Contains(string(raw), "private") {
				t.Fatalf("incorrect member projection: %s", raw)
			}
		})
	}
}

func TestMemberListsRejectMissingDuplicateAndContradictoryIdentity(t *testing.T) {
	for _, rows := range [][]object{
		{{"first_name": "Synthetic"}},
		{{"member_guid": "u0member"}, {"member_guid": "u0member"}},
		{{"member_guid": "u0member", "member_type": "Channel"}},
	} {
		c, _ := newRPCFixture(t, func(string, object, bool) object { return object{"in_chat_members": rows} })
		if _, err := c.Call(context.Background(), "group.members", json.RawMessage(`{"peer":{"type":"group","id":"g0test"}}`)); err == nil {
			t.Fatal("invalid member identity accepted")
		}
	}
}
