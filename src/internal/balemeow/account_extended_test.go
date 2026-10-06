package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func TestAccountExtensionMutationsUseReviewedFields(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var response proto.Message = &wire.Empty{}
		switch r.Method {
		case "AddContact", "RemoveContact":
			if !bytes.Equal(r.Payload, []byte{8, 42, 16, 99}) {
				t.Errorf("uid/hash wire fields incorrect: %x", r.Payload)
			}
		case "EditUserLocalName":
			v := &wire.AccountLocalNameRequest{}
			if decode(r.Payload, v) != nil || v.Uid != 42 || v.AccessHash != 99 || v.Name != "local" {
				t.Error("wrong rename")
			}
		case "BlockUser", "UnblockUser":
			if !bytes.Equal(r.Payload, []byte{10, 4, 8, 42, 16, 99}) {
				t.Error("block must carry OutUserPeer, not InPeer")
			}
		case "EditAbout", "EditNickName":
			if !bytes.Equal(r.Payload, []byte{10, 0}) {
				t.Error("clearing wrapper must remain present")
			}
		case "EditName":
			if !bytes.Equal(r.Payload, []byte{10, 1, 'N'}) {
				t.Error("wrong name field")
			}
		case "ImportContacts":
			v := &wire.AccountImportRequest{}
			if decode(r.Payload, v) != nil || len(v.Phones) != 1 || v.Phones[0].Phone != 989121234567 || v.Phones[0].Name.GetValue() != "N" {
				t.Error("wrong contact import")
			}
			response = &wire.AccountImportResponse{Users: []*wire.User{{Id: 42, AccessHash: 99, Name: "N"}}}
		case "SetUserPrivacyStatus":
			v := &wire.AccountPrivacyRequest{}
			if decode(r.Payload, v) != nil || v.UserId != 12345 || v.Type != 1 || v.Status != 2 {
				t.Error("privacy escaped own account")
			}
		case "EditParameter":
			v := &wire.AccountSettingRequest{}
			if decode(r.Payload, v) != nil || v.Key != "app.web.test" || v.Value != nil {
				t.Error("null must clear parameter")
			}
		case "GetAuthSessions":
			response = &wire.AccountSessionsResponse{Sessions: []*wire.AccountAuthSession{{Id: 7, AppTitle: "test"}}}
		case "TerminateSession":
			if !bytes.Equal(r.Payload, []byte{8, 7}) {
				t.Error("wrong session selector")
			}
		case "TerminateAllSessions", "ResetContacts":
			if len(r.Payload) != 0 {
				t.Error("request must be empty")
			}
		default:
			t.Errorf("unexpected mutation %s", r.Method)
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 99})
	for _, tc := range []struct{ op, body string }{
		{"contacts.add", `"user":{"type":"user","id":"42"}`}, {"contacts.remove", `"user":{"type":"user","id":"42"}`}, {"contacts.rename", `"user":{"type":"user","id":"42"},"name":"local"`},
		{"account.block", `"user":{"type":"user","id":"42"}`}, {"account.unblock", `"user":{"type":"user","id":"42"}`},
		{"account.name", `"name":"N"`}, {"account.about", `"about":""`}, {"account.username", `"username":""`},
		{"contacts.import", `"contacts":[{"phone":"09121234567","name":"N"}]`}, {"contacts.reset", `"confirm":true`},
		{"account.privacy.set", `"type":1,"status":2`}, {"account.settings.set", `"key":"app.web.test","value":null`},
		{"account.session.terminate", `"session_id":"7"`}, {"account.sessions.terminate", `"confirm":true`},
	} {
		raw, e := c.accountExtendedCall(context.Background(), tc.op, json.RawMessage(`{"request_id":"9007199254740993",`+tc.body+`}`))
		if e != nil || !strings.Contains(string(raw), `"acknowledged":true`) || strings.Contains(string(raw), "access_hash") {
			t.Fatalf("%s: %s %v", tc.op, raw, e)
		}
	}
}

func TestAccountExtensionReadsAndCorrectFullUserSchema(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var response proto.Message
		switch r.Method {
		case "LoadUsers":
			q := &wire.AccountPeersRequest{}
			_ = decode(r.Payload, q)
			response = &wire.AccountUsersResponse{Users: []*wire.User{{Id: q.Peers[0].Id, AccessHash: 99, Name: "Synthetic", Nick: &wire.StringValue{Value: "synthetic"}}}}
		case "LoadFullUsers":
			response = &wire.AccountFullUsersResponse{Users: []*wire.AccountFullUser{{Id: 42, About: &wire.StringValue{Value: "bio"}, IsContact: &wire.BoolValue{Value: true}}}}
		case "GetFullUser":
			response = &wire.AccountFullUserResponse{User: &wire.FullUser{Id: 12345, About: &wire.StringValue{Value: "bio"}, Languages: []string{"fa"}, Timezone: &wire.StringValue{Value: "Asia/Tehran"}}}
		case "CheckNickName":
			response = &wire.BoolValue{Value: true}
		case "LoadBlockedUsers":
			response = &wire.AccountPeersResponse{Peers: []*wire.PeerRef{{Id: 42, AccessHash: 99}}}
		case "GetUserFullPrivacy":
			response = &wire.AccountPrivacyResponse{Privacy: &wire.AccountPrivacy{Invite: 1, Presence: 2}}
		case "GetUserPrivacyStatus":
			response = &wire.AccountPrivacyStatus{Status: 2}
		case "GetParameters":
			response = &wire.AccountSettingsResponse{Parameters: []*wire.AccountSetting{{Key: "app.web.theme", Value: "dark"}, {Key: "auth_token", Value: "must-not-leak"}}}
		case "GetAuthSessions":
			response = &wire.AccountSessionsResponse{Sessions: []*wire.AccountAuthSession{{Id: 7, AuthHolder: 123, AppId: 42, AppTitle: "Desktop", DeviceTitle: "Synthetic", LastIpAddress: &wire.StringValue{Value: "must-not-leak"}}}}
		default:
			t.Errorf("unexpected read %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 99})
	for _, tc := range []struct{ op, body, want string }{
		{"account.info", `{}`, `"about":"bio"`}, {"users.get", `{"users":[{"type":"user","id":"42"}]}`, `"name":"Synthetic"`}, {"users.get", `{"users":[{"type":"user","id":"42"}],"full":true}`, `"is_contact":true`},
		{"account.username.check", `{"username":"test"}`, `"available":true`}, {"account.blocked", `{}`, `"id":"42"`}, {"account.privacy", `{}`, `"presence":2`}, {"account.privacy.status", `{"type":1}`, `"status":2`},
		{"account.settings", `{}`, `"redacted_count":1`}, {"account.sessions", `{}`, `"session_id":"7"`},
	} {
		raw, e := c.accountExtendedCall(context.Background(), tc.op, json.RawMessage(tc.body))
		if e != nil || !strings.Contains(string(raw), tc.want) || strings.Contains(string(raw), "must-not-leak") || strings.Contains(string(raw), "access_hash") {
			t.Fatalf("%s: %s %v", tc.op, raw, e)
		}
	}
}

func TestAccountExtensionsValidateBeforeMutationAndNeverRetry(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body, code string }{
		{"account.name", `{"name":"N"}`, "INVALID_REQUEST_ID"}, {"account.name", `{"name":"","request_id":"1"}`, "INVALID_REQUEST"},
		{"contacts.add", `{"request_id":"1","user":{"type":"user","id":"42","access_hash":"999"}}`, "INVALID_REQUEST"},
		{"contacts.reset", `{"request_id":"1"}`, "INVALID_REQUEST"}, {"account.privacy.set", `{"request_id":"1","user":{"type":"user","id":"42"},"type":1,"status":2}`, "INVALID_REQUEST"},
		{"account.settings.set", `{"request_id":"1","key":"auth.token","value":"secret"}`, "INVALID_REQUEST"}, {"account.settings.set", `{"request_id":"1","key":"theme"}`, "INVALID_REQUEST"},
	} {
		_, e := c.accountExtendedCall(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(e) != tc.code {
			t.Fatalf("%s: %v", tc.op, e)
		}
	}
	var count atomic.Int32
	f := newFakeWS(t, func(_ *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			count.Add(1)
		}
	})
	c = f.client()
	c.opts.RequestTimeout = 30 * time.Millisecond
	connectTest(t, c, acceptingSink)
	_, err := c.accountExtendedCall(context.Background(), "account.name", json.RawMessage(`{"request_id":"1","name":"N"}`))
	if codeOf(err) != "SEND_UNKNOWN" || count.Load() != 1 {
		t.Fatalf("ambiguous mutation retried: %v count=%d", err, count.Load())
	}
}

func TestResolvePhoneRequiresExactProviderPhoneField(t *testing.T) {
	for _, mode := range []string{"exact", "missing", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				r := m.Request
				if r.Method != "SearchContacts" {
					t.Errorf("phone resolution mutated contacts: %s", r.Method)
				}
				users := []*wire.AccountSearchUser{{Id: 1, AccessHash: 11}}
				if mode != "missing" {
					users = append(users, &wire.AccountSearchUser{Id: 42, AccessHash: 99, ContactInfo: []*wire.AccountContactRecord{{Type: 0, LongValue: &wire.Int64Value{Value: 989121234567}}}})
				}
				if mode == "ambiguous" {
					users = append(users, &wire.AccountSearchUser{Id: 43, AccessHash: 98, ContactInfo: []*wire.AccountContactRecord{{Type: 0, StringValue: &wire.StringValue{Value: "+989121234567"}}}})
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.AccountSearchResponse{Users: users})}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			peer, err := c.ResolvePhone(context.Background(), "09121234567")
			if mode == "exact" {
				if err != nil || peer.ID != "42" || peer.AccessHash != "" {
					t.Fatalf("%v %v", peer, err)
				}
			} else {
				want := "PHONE_NOT_FOUND"
				if mode == "ambiguous" {
					want = "PHONE_AMBIGUOUS"
				}
				if codeOf(err) != want {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestFullUserRPCVariantsUseDifferentWireLayouts(t *testing.T) {
	// Synthetic literal protobuf bytes using the independently reviewed field
	// numbers: batch metadata deleted=12/contact=13; full identity nick=5/about=7.
	batch := &wire.AccountFullUser{}
	if err := decode([]byte{8, 42, 98, 2, 8, 1, 106, 2, 8, 1}, batch); err != nil || !batch.GetIsDeleted().GetValue() || !batch.GetIsContact().GetValue() {
		t.Fatal("batch full-user flags use the wrong layout", batch, err)
	}
	full := &wire.FullUser{}
	if err := decode([]byte{8, 42, 42, 5, 10, 3, 'n', 'i', 'c', 58, 5, 10, 3, 'b', 'i', 'o', 114, 2, 8, 1, 122, 2, 8, 1}, full); err != nil || full.GetNick().GetValue() != "nic" || full.GetAbout().GetValue() != "bio" || !full.GetIsDeleted().GetValue() || !full.GetIsContact().GetValue() {
		t.Fatal("GetFullUser identity layout invalid", full, err)
	}
}
