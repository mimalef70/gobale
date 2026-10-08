package balemeow

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestAccountGroupInfoAndInviteLink(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var response proto.Message
		switch r.Method {
		case "LoadUsers":
			p := &wire.AccountPeersRequest{}
			if decode(r.Payload, p) != nil || len(p.Peers) != 1 || p.Peers[0].Id != 12345 {
				t.Error("account basic lookup escaped session")
			}
			response = &wire.AccountUsersResponse{Users: []*wire.User{{Id: 12345, AccessHash: 99, Name: "Synthetic self", Nick: &wire.StringValue{Value: "testself"}}}}
		case "GetFullUser":
			p := &wire.GetUserInfoRequest{}
			_ = decode(r.Payload, p)
			if p.Peer.Id != 12345 {
				t.Fatal("account lookup escaped session")
			}
			response = &wire.AccountFullUserResponse{User: &wire.FullUser{Id: 12345, About: &wire.StringValue{Value: "example"}}}
		case "GetFullGroup":
			p := &wire.GetGroupInfoRequest{}
			_ = decode(r.Payload, p)
			if p.Peer.Id != 77 || p.Peer.AccessHash != 19 {
				t.Error("missing private group ref")
			}
			response = &wire.GetGroupInfoResponse{Group: &wire.FullGroup{Id: 77, AccessHash: 19, Title: "Synthetic group", OwnerUid: 42, CreateDate: 1720000000000, MembersCount: &wire.Int32Value{Value: 5}, About: &wire.StringValue{Value: "Example group"}}}
		case "GetGroupInviteURL":
			response = &wire.GroupInviteURLResponse{Url: "https://example.invalid/synthetic-only"}
		default:
			t.Errorf("unexpected read %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("group", &wire.PeerRef{Id: 77, AccessHash: 19})
	for _, tc := range []struct{ op, body, want string }{
		{"account.info", `{}`, `"account_id":"12345"`},
		{"group.info", `{"peer":{"type":"group","id":"77"}}`, `"members_count":5`},
		{"group.link", `{"peer":{"type":"group","id":"77"}}`, `"invite_link":"https://example.invalid/synthetic-only"`},
	} {
		raw, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
		if err != nil || !strings.Contains(string(raw), tc.want) || strings.Contains(string(raw), "access_hash") {
			t.Fatalf("%s: %s %v", tc.op, raw, err)
		}
	}
}

func TestHistoryRegistersPrivateMediaBeforePublishing(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.HistoryResponse{History: []*wire.HistoryItem{{SenderId: 42, Rid: 123, Date: 1720000000000, Message: &wire.Message{Document: &wire.DocumentMessage{FileId: -123456789012, AccessHash: 987654321, FileSize: 4, Name: "test.txt", MimeType: "text/plain"}}}}})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	saved := false
	c.opts.SaveMediaReference = func(ctx context.Context, peer domains.Peer, id string, m domains.ProviderMedia) (bool, error) {
		if peer.ID != "42" || id != "123" || m.FileID != "-123456789012" || m.AccessHash != "987654321" {
			t.Fatal("bad private ref")
		}
		saved = true
		return true, nil
	}
	raw, err := c.Call(context.Background(), "chat.history", json.RawMessage(`{"peer":{"type":"user","id":"42"}}`))
	if err != nil || !saved || strings.Contains(string(raw), "987654321") || !strings.Contains(string(raw), `"download_supported":true`) {
		t.Fatalf("%s %v", raw, err)
	}
	// A newer edit or deletion can prevent a history reference from becoming
	// current without being a storage error. Keep the attachment, but do not
	// advertise that downloading it under this message ID is supported.
	c.opts.SaveMediaReference = func(context.Context, domains.Peer, string, domains.ProviderMedia) (bool, error) { return false, nil }
	raw, err = c.Call(context.Background(), "chat.history", json.RawMessage(`{"peer":{"type":"user","id":"42"}}`))
	if err != nil || strings.Contains(string(raw), `"download_supported":true`) || !strings.Contains(string(raw), `"file_id":"-123456789012"`) || !strings.Contains(string(raw), `"name":"test.txt"`) {
		t.Fatalf("ignored registration advertised a download or erased attachment metadata: %s %v", raw, err)
	}
	c.opts.SaveMediaReference = func(context.Context, domains.Peer, string, domains.ProviderMedia) (bool, error) {
		return false, domains.E("STORAGE_FAILED", "synthetic failure", 500)
	}
	raw, err = c.Call(context.Background(), "chat.history", json.RawMessage(`{"peer":{"type":"user","id":"42"}}`))
	if err == nil || len(raw) != 0 {
		t.Fatalf("persistence failure became successful history: %s %v", raw, err)
	}
}
