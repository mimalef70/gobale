package balemeow

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestGroupPermissionPatchPreservesOmittedAndFutureFields(t *testing.T) {
	for _, member := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "member"}[member], func(t *testing.T) {
			var writes atomic.Int32
			future := protowire.AppendTag(nil, 71, protowire.VarintType)
			future = protowire.AppendVarint(future, 1)
			original := &wire.GroupPermissions{SeeMessage: true, SendMessage: true, InviteUser: false, SendMedia: &wire.BoolValue{Value: true}, SendLinkMessage: &wire.BoolValue{Value: false}}
			original.ProtoReflect().SetUnknown(future)
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				r := m.Request
				var reply proto.Message = &wire.Empty{}
				switch r.Method {
				case "GetFullGroup":
					reply = &wire.GroupFullExtendedResponse{Group: &wire.GroupFullExtended{Id: 77, DefaultPermissions: original}}
				case "GetMemberPermissions":
					reply = &wire.GroupMemberPermissionsResponse{Permissions: original}
				case "SetGroupDefaultPermissions", "SetMemberPermissions":
					var got *wire.GroupPermissions
					if member {
						q := &wire.GroupSetMemberPermissionsRequest{}
						if decode(r.Payload, q) != nil || q.User.GetId() != 42 || q.Group.GetAccessHash() != 123 {
							t.Error("incorrect member reference")
						}
						got = q.Permissions
					} else {
						q := &wire.GroupSetDefaultPermissionsRequest{}
						if decode(r.Payload, q) != nil || q.Group.GetAccessHash() != 123 {
							t.Error("incorrect group reference")
						}
						got = q.Permissions
					}
					if got == nil || !got.SeeMessage || !got.SendMessage || !got.InviteUser || !got.GetSendMedia().GetValue() || got.SendLinkMessage == nil || got.SendLinkMessage.Value || string(got.ProtoReflect().GetUnknown()) != string(future) {
						t.Errorf("patch changed omitted fields: %v", got)
					}
					writes.Add(1)
				default:
					t.Errorf("unexpected %s", r.Method)
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, reply)}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			c.rememberRef("group", &wire.PeerRef{Id: 77, AccessHash: 123})
			c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 456})
			op := "group.default_permissions.set"
			if member {
				op = "group.permissions.set"
			}
			body := `{"peer":{"type":"group","id":"77"},"user":{"type":"user","id":"42"},"permissions":{"invite_user":true},"request_id":"9007199254740993"}`
			result, err := c.groupExtended(context.Background(), op, json.RawMessage(body))
			if err != nil || writes.Load() != 1 || !strings.Contains(string(result), `"invite_user":true`) || strings.Contains(string(result), "access_hash") {
				t.Fatalf("%s %v", result, err)
			}
		})
	}
}

func TestExtendedGroupValidationPrecedesNetwork(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body, code string }{
		{"group.history", `{"peer":{"type":"group","id":"77"},"show":true}`, "INVALID_REQUEST_ID"},
		{"group.history", `{"peer":{"type":"group","id":"77"},"request_id":"1"}`, "INVALID_REQUEST"},
		{"group.permissions.set", `{"user":{"type":"user","id":"42"},"permissions":{"send_message":null},"request_id":"1"}`, "INVALID_REQUEST"},
		{"group.default_permissions.set", `{"permissions":{"invte_user":true},"request_id":"1"}`, "INVALID_REQUEST"},
		{"group.default_permissions.set", `{"permissions":{"send_message":true},"mode":"replace","request_id":"1"}`, "INVALID_REQUEST"},
		{"group.pin", `{"message_id":"0","date":"1720000000000","sender_id":"42","request_id":"1"}`, "INVALID_REQUEST"},
		{"group.join", `{"token":"https://example.com/join/foo","request_id":"1"}`, "INVALID_REQUEST"},
		{"group.restriction", `{"restriction":"public","request_id":"1"}`, "INVALID_REQUEST"},
		{"group.photo", `{"request_id":"1"}`, "INVALID_REQUEST"},
		{"group.promote", `null`, "INVALID_REQUEST"},
		{"group.promote", `{"unknown":true}`, "INVALID_REQUEST"},
	} {
		_, err := c.groupExtended(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(err) != tc.code {
			t.Errorf("%s: %v", tc.op, err)
		}
	}
	values := map[string]*bool{}
	f := false
	for _, name := range permissionNames {
		values[name] = &f
	}
	if err := validatePermissionPatch(values, "replace"); err != nil {
		t.Fatal(err)
	}
}

func TestGroupExtensionOfficialRequestsAndBoundedResults(t *testing.T) {
	var methods []string
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		methods = append(methods, r.Method)
		if r.Service != groupsService {
			t.Errorf("wrong service %s", r.Service)
		}
		var reply proto.Message = &wire.Empty{}
		switch r.Method {
		case "MakeUserAdmin", "RemoveUserAdmin", "UnBanUser":
			q := &wire.GroupAdminRequest{}
			if decode(r.Payload, q) != nil || q.Group.GetAccessHash() != 123 || q.User.GetAccessHash() != 456 {
				t.Error("lost private references")
			}
		case "TransferOwnership":
			q := &wire.GroupTransferRequest{}
			if decode(r.Payload, q) != nil || q.NewOwner != 42 {
				t.Error("wrong owner")
			}
		case "SetCanSeeHistory":
			q := &wire.GroupHistoryRequest{}
			if decode(r.Payload, q) != nil || q.CanSeeHistory || q.Group.GetId() != 77 {
				t.Error("explicit false not preserved")
			}
		case "SetCanSeeMessages":
			q := &wire.GroupVisibilityRequest{}
			if decode(r.Payload, q) != nil || !q.CanSeeMessages || q.UserId != 42 {
				t.Error("wrong visibility")
			}
		case "EditChannelNick":
			q := &wire.GroupUsernameRequest{}
			if decode(r.Payload, q) != nil || q.Nick != "synthetic" || q.Rid != 9007199254740993 {
				t.Error("wrong username/RID")
			}
		case "SetRestriction":
			q := &wire.GroupRestrictionRequest{}
			if decode(r.Payload, q) != nil || q.Restriction != 1 || q.GetNick().GetValue() != "synthetic" {
				t.Error("wrong restriction")
			}
		case "PinMessage":
			q := &wire.GroupPinRequest{}
			if decode(r.Payload, q) != nil || q.Rid != -99 || q.Date != 1720000000000 || q.SenderUid != 42 || q.Group.GetId() != 77 {
				t.Error("wrong pin")
			}
		case "RemoveSinglePin":
			q := &wire.GroupUnpinRequest{}
			if decode(r.Payload, q) != nil || q.Rid != -99 || q.Date != 1720000000000 {
				t.Error("wrong unpin")
			}
		case "RemovePin":
		case "RevokeInviteURL":
			reply = &wire.GroupInviteURLResponse{Url: "https://ble.ir/join/synthetic"}
		case "GetPins":
			q := &wire.GroupPinsRequest{}
			if decode(r.Payload, q) != nil || q.Page != 1 || q.Limit != 20 {
				t.Error("wrong pin pagination")
			}
			reply = &wire.GroupPinsResponse{Count: 1, Pins: []*wire.GroupPinnedMessage{{SenderUid: 42, Rid: -99, Date: 1720000000000, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}}}}
		case "GetBannedUsers":
			reply = &wire.GroupBannedResponse{BannedUsers: []*wire.GroupBanRecord{{BannedUser: &wire.PeerRef{Id: 42, AccessHash: 456}, BannerUser: &wire.PeerRef{Id: 9, AccessHash: 3}}}}
		case "FetchGroupAdmins":
			reply = &wire.GroupAdminsResponse{Users: []*wire.PeerRef{{Id: 42, AccessHash: 456}}, Admins: []*wire.GroupExtendedMember{{Uid: 42, IsAdmin: &wire.BoolValue{Value: true}, Title: &wire.StringValue{Value: "Support"}}}}
		case "RemoveGroupAvatar":
			q := &wire.GroupRemovePhotoRequest{}
			if decode(r.Payload, q) != nil || q.Rid != 9007199254740993 || q.GetAvatarId().GetValue() != -9 {
				t.Error("wrong avatar removal")
			}
		case "LeaveGroup":
			q := &wire.GroupLeaveRequest{}
			if decode(r.Payload, q) != nil || q.Rid != 9007199254740993 || q.MakeOrphan == nil || q.MakeOrphan.Value {
				t.Error("wrong leave")
			}
		default:
			t.Errorf("unexpected call %s", r.Method)
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, reply)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("channel", &wire.PeerRef{Id: 77, AccessHash: 123})
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 456})
	tests := []struct{ op, extra, want string }{
		{"group.promote", `,"user":{"type":"user","id":"42"},"admin_title":"Support"`, "acknowledged"},
		{"group.demote", `,"user":{"type":"user","id":"42"}`, "acknowledged"},
		{"group.transfer", `,"user":{"type":"user","id":"42"}`, "acknowledged"},
		{"group.history", `,"show":false`, "acknowledged"},
		{"group.visibility", `,"user":{"type":"user","id":"42"},"can_see_messages":true`, "acknowledged"},
		{"group.username", `,"username":"synthetic"`, "acknowledged"},
		{"group.restriction", `,"restriction":"public","username":"synthetic"`, "acknowledged"},
		{"group.pin", `,"message_id":"-99","date":"1720000000000","sender_id":"42"`, "acknowledged"},
		{"group.unpin", `,"message_id":"-99","date":"1720000000000"`, "acknowledged"},
		{"group.unpin_all", ``, "acknowledged"},
		{"group.link.revoke", ``, "https://ble.ir/join/synthetic"},
		{"group.pins", ``, `"message_id":"-99"`},
		{"group.banned", ``, `"banned_by":"9"`},
		{"group.admins", ``, `"title":"Support"`},
		{"group.unban", `,"user":{"type":"user","id":"42"}`, "acknowledged"},
		{"group.photo.remove", `,"avatar_id":"-9"`, "acknowledged"},
		{"group.leave", `,"make_orphan":false`, "acknowledged"},
	}
	for _, tc := range tests {
		body := `{"peer":{"type":"channel","id":"77"},"request_id":"9007199254740993"` + tc.extra + `}`
		data, err := c.groupExtended(context.Background(), tc.op, json.RawMessage(body))
		if err != nil || !strings.Contains(string(data), tc.want) || strings.Contains(string(data), "access_hash") {
			t.Fatalf("%s: %s %v", tc.op, data, err)
		}
	}
	if c.canonicalPeer(domains.Peer{Type: "group", ID: "77"}).Type != "group" {
		t.Fatal("leave did not clear private group references")
	}
}

func TestChannelCreationDiscoveryAndJoinCanonicalIdentity(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var reply proto.Message
		switch r.Method {
		case "CreateGroup":
			q := &wire.GroupCreateExtendedRequest{}
			if decode(r.Payload, q) != nil || q.GroupType != 1 || q.Restriction != 1 || q.GetNick().GetValue() != "synthetic" || q.Rid != 99 {
				t.Error("wrong channel creation")
			}
			reply = &wire.GroupCreateResponse{Group: &wire.Group{Id: 88, AccessHash: 123, GroupType: 1, Title: "Synthetic"}}
		case "GetMyGroups":
			q := &wire.GetMyGroupsRequest{}
			if decode(r.Payload, q) != nil || q.Mode != 2 {
				t.Error("channel discovery requested groups")
			}
			reply = &wire.GetMyGroupsResponse{Groups: []*wire.PeerRef{{Id: 89, AccessHash: 456}}}
		case "JoinGroup":
			q := &wire.GroupJoinRequest{}
			if decode(r.Payload, q) != nil || q.Token != "synthetic" {
				t.Error("bad join token")
			}
			reply = &wire.GroupJoinResponse{Group: &wire.Group{Id: 90, AccessHash: 789, GroupType: 1, Title: "Joined"}}
		case "GetGroupPreview":
			reply = &wire.GroupFullExtendedResponse{Group: &wire.GroupFullExtended{Id: 90, AccessHash: 789, GroupType: 1, Title: "Joined"}}
		default:
			t.Errorf("unexpected %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, reply)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	created, err := c.groupCall(context.Background(), "channel.create", json.RawMessage(`{"title":"Synthetic","username":"synthetic","request_id":"99"}`))
	if err != nil || !strings.Contains(string(created), `"type":"channel"`) {
		t.Fatalf("%s %v", created, err)
	}
	ref, err := c.groupRef(context.Background(), domains.Peer{Type: "channel", ID: "89"})
	if err != nil || ref.AccessHash != 456 {
		t.Fatalf("%v %v", ref, err)
	}
	for _, op := range []string{"group.join", "group.preview"} {
		data, err := c.groupExtended(context.Background(), op, json.RawMessage(`{"token":"https://ble.ir/join/synthetic","request_id":"100"}`))
		if err != nil || !strings.Contains(string(data), `"type":"channel"`) || strings.Contains(string(data), "access_hash") {
			t.Fatalf("%s %v", data, err)
		}
	}
	for _, id := range []string{"88", "89", "90"} {
		if c.canonicalPeer(domains.Peer{Type: "group", ID: id}).Type != "channel" {
			t.Fatal("lost subtype", id)
		}
	}
	if c.canonicalPeer(domains.Peer{Type: "user", ID: "90"}).Type != "user" {
		t.Fatal("cross-kind collision")
	}
}
