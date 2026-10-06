package balemeow

import (
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"strings"
	"testing"
)

func TestNestedTemplateQuoteAndHistoryPreservePublicContent(t *testing.T) {
	text := "سلام\u200cدوست ✅ literal \\n"
	m := &wire.Message{Template: &wire.TemplateMessage{Id: -9223372036854775807, Message: &wire.Message{Text: &wire.TextMessage{Text: text, Mentions: []uint32{4000000000}}}, InlineKeyboard: &wire.InlineKeyboard{Rows: []*wire.InlineRow{{Buttons: []*wire.InlineButton{{Text: "ورود", Url: &wire.StringValue{Value: "https://example.test"}, CallbackData: &wire.StringValue{Value: "literal-action"}, CopyText: &wire.CopyTextButton{Text: "copy"}}}}}}}}
	q := &wire.QuotedMessage{MessageId: &wire.Int64Value{Value: -33}, SenderUserId: 42, Date: 1720000000000, Message: &wire.Message{Text: &wire.TextMessage{Text: "quoted"}}, Peer: &wire.Peer{Type: 1, Id: 42, AccessHash: 7654321098765}}
	raw := marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 1, Id: 42, AccessHash: 7654321098765}, SenderId: 42, Rid: -55, Date: 1720000000001, Message: m, QuotedMessage: q, GroupedId: &wire.Int64Value{Value: 9223372036854775807}}})
	events, e := decodeEvents("12345", raw)
	if e != nil || len(events) != 1 {
		t.Fatalf("decode %v %v", events, e)
	}
	var p map[string]any
	if e = json.Unmarshal(events[0].Payload, &p); e != nil {
		t.Fatal(e)
	}
	if p["kind"] != "template" || p["template_id"] != "-9223372036854775807" || p["grouped_id"] != "9223372036854775807" {
		t.Fatalf("lost IDs: %s", events[0].Payload)
	}
	content := p["content"].(map[string]any)
	if content["message"] != text || content["mentions"].([]any)[0] != "4000000000" {
		t.Fatalf("content changed: %#v", content)
	}
	if strings.Contains(string(events[0].Payload), "7654321098765") || strings.Contains(string(events[0].Payload), "access_hash") {
		t.Fatal("private ref leaked")
	}
	h := &wire.HistoryItem{SenderId: 42, Rid: -55, Date: 1720000000001, Message: &wire.Message{Empty: &wire.Empty{}}, QuotedMessage: q, EditedAt: &wire.Int64Value{Value: 1720000000002}, Next: &wire.MessagePosition{Rid: 99, Date: 1720000000003}}
	var hp map[string]any
	_ = json.Unmarshal(decoratedHistoryPayload(h, false), &hp)
	if hp["kind"] != "forward" || hp["quoted_message"].(map[string]any)["message_id"] != "-33" {
		t.Fatalf("lost forward metadata: %#v", hp)
	}
}
func TestReceiveNestedMediaPrivateReferenceAndBounds(t *testing.T) {
	m := &wire.Message{Template: &wire.TemplateMessage{Message: &wire.Message{Document: &wire.DocumentMessage{FileId: -9, AccessHash: 12345, FileSize: 4, Name: "a.txt", MimeType: "text/plain"}}}}
	if e := validateMessage(m); e != nil {
		t.Fatal(e)
	}
	if p := providerMedia(m); p == nil || p.AccessHash != "12345" || p.FileID != "-9" {
		t.Fatal("private descriptor missing")
	}
	h := &wire.HistoryItem{Message: m}
	raw := decoratedHistoryPayload(h, true)
	if !strings.Contains(string(raw), `"download_supported":true`) || strings.Contains(string(raw), "12345") {
		t.Fatalf("incorrect media projection %s", raw)
	}
	for i := 0; i < maxContentDepth; i++ {
		m = &wire.Message{Template: &wire.TemplateMessage{Message: m}}
	}
	if validateMessage(m) == nil {
		t.Fatal("unbounded nesting")
	}
	tooMany := &wire.Message{Template: &wire.TemplateMessage{Buttons: make([]*wire.TemplateButton, 1025)}}
	for i := range tooMany.Template.Buttons {
		tooMany.Template.Buttons[i] = &wire.TemplateButton{}
	}
	if validateMessage(tooMany) == nil {
		t.Fatal("button expansion limit absent")
	}
	if validateMessage(&wire.Message{Text: &wire.TextMessage{}, Empty: &wire.Empty{}}) == nil {
		t.Fatal("contradictory message union accepted")
	}
}
func TestUsefulUpdatesAndBatchedMessages(t *testing.T) {
	peer := &wire.Peer{Type: 1, Id: 42}
	cases := []struct {
		u    *wire.UpdateContainer
		kind string
	}{
		{&wire.UpdateContainer{ChatCleared: &wire.ChatPeerUpdate{Peer: peer}}, "chat.cleared"},
		{&wire.UpdateContainer{ChatDeleted: &wire.ChatPeerUpdate{Peer: peer}}, "chat.deleted"},
		{&wire.UpdateContainer{Read: &wire.MessageReceipt{Peer: peer, StartDate: 1700000000000, Date: 1700000000001}}, "message.read"},
		{&wire.UpdateContainer{ReadByMe: &wire.ReadByMeUpdate{Peer: peer, StartDate: 1700000000000, UnreadCount: &wire.Int32Value{Value: 0}}}, "message.read_by_me"},
		{&wire.UpdateContainer{Received: &wire.MessageReceipt{Peer: peer, StartDate: 1700000000000, Date: 1700000000001}}, "message.received"},
		{&wire.UpdateContainer{UsernameChanged: &wire.UserTextUpdate{Uid: 42, Value: &wire.StringValue{Value: "new"}}}, "user.username_changed"},
		{&wire.UpdateContainer{Blocked: &wire.UserIDUpdate{Uid: 42}}, "user.blocked"},
		{&wire.UpdateContainer{Unblocked: &wire.UserIDUpdate{Uid: 42}}, "user.unblocked"},
		{&wire.UpdateContainer{Unpinned: &wire.GroupUnpinnedUpdate{GroupId: 99}}, "message.unpinned"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			raw := marshal(t, c.u)
			a, e := decodeEvents("12345", raw)
			if e != nil || len(a) != 1 || a[0].Type != c.kind {
				t.Fatalf("got %v err %v", a, e)
			}
			b, _ := decodeEvents("12345", raw)
			if a[0].ID != b[0].ID {
				t.Fatal("unstable replay ID")
			}
		})
	}
	m := &wire.HistoryItem{SenderId: 42, Rid: -99, Date: 1700000000001, Message: &wire.Message{Text: &wire.TextMessage{Text: "batch"}}}
	raw := marshal(t, &wire.UpdateContainer{Messages: &wire.MultipleMessagesUpdate{Peer: peer, Messages: []*wire.HistoryItem{m}}})
	a, e := decodeEvents("12345", raw)
	if e != nil || len(a) != 1 || a[0].Type != "message" || a[0].MessageID != "-99" {
		t.Fatalf("batch decode %v %v", a, e)
	}
	direct, _ := decodeEvents("12345", marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: peer, SenderId: 42, Rid: m.Rid, Date: m.Date, Message: m.Message}}))
	if a[0].ID != direct[0].ID {
		t.Fatal("batch event must dedupe with live echo")
	}
}
func TestGiftAndServiceProjectionDoesNotLeakWallet(t *testing.T) {
	m := &wire.Message{Gift: &wire.GiftMessage{Count: 2, TotalAmount: 100000, OwnerId: 42, WalletId: &wire.StringValue{Value: "private-wallet"}, ShowTotalAmount: &wire.BoolValue{Value: false}, Regarding: &wire.StringValue{Value: "هدیه"}}}
	raw := messagePayload(m)
	if strings.Contains(string(raw), "private-wallet") || strings.Contains(string(raw), "100000") {
		t.Fatal("hidden gift fields exposed")
	}
	s := &wire.Message{Service: &wire.ServiceMessage{Text: "changed", Ext: &wire.ServiceExtensions{UserInvited: &wire.UserIDUpdate{Uid: 4000000000}, TitleChanged: &wire.ServiceTitle{Title: "title"}}}}
	first := string(messagePayload(s))
	for i := 0; i < 100; i++ {
		if string(messagePayload(s)) != first {
			t.Fatal("service fingerprint unstable")
		}
	}
}

func TestGroupUpdatesPreservePermissionsWithoutAccessHashes(t *testing.T) {
	raw := marshal(t, &wire.GroupUpdateUnion{Title: &wire.GroupStringUpdate{GroupId: 99, Value: "new name"}, DefaultPermissions: &wire.GroupDefaultPermissionUpdate{Group: &wire.PeerRef{Id: 99, AccessHash: 12345678999}, Permissions: &wire.GroupPermissions{InviteUser: true, SendMedia: &wire.BoolValue{Value: false}}}})
	e, err := decodeEvents("12345", raw)
	if err != nil || len(e) != 2 {
		t.Fatalf("group update %v %v", e, err)
	}
	for _, v := range e {
		if v.Peer.ID != "99" || strings.Contains(string(v.Payload), "12345678999") || strings.Contains(string(v.Payload), "access_hash") {
			t.Fatalf("invalid public update %+v", v)
		}
	}
}
func TestPresenceSchemaAndRepeatedStateEventIdentity(t *testing.T) {
	raw := marshal(t, &wire.PresenceUpdateUnion{Offline: &wire.PresenceStateUpdate{UserId: 42, DeviceType: 2, DeviceCategory: &wire.StringValue{Value: "web"}}})
	decodeAt := func(stamp int64) []domains.Event {
		v, e := decodeStreamEvents("12345", marshal(t, &wire.StreamUpdate{RouteId: 4, Sequence: 10, Timestamp: stamp, Update: raw}))
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	a := decodeAt(1720000000000)
	b := decodeAt(1720000000001)
	again := decodeAt(1720000000000)
	if a[0].Type != "presence.offline" || strings.Contains(string(a[0].Payload), "date") {
		t.Fatalf("offline f2 misread as timestamp: %s", a[0].Payload)
	}
	if a[0].ID == b[0].ID || a[0].ID != again[0].ID {
		t.Fatal("repeated state or replay identity broken")
	}
	dated, e := decodeEvents("12345", raw)
	if e != nil {
		t.Fatal(e)
	}
	stateEventsPosition(dated, "4|date:1720000000000")
	if dated[0].ID != a[0].ID {
		t.Fatal("dated catch-up not matched with live")
	}
}
func TestAnonymousPollDoesNotExposeVoters(t *testing.T) {
	m := &wire.Message{Poll: &wire.PollMessage{Question: "Yes?", PollId: 9223372036854775807, IsAnonymous: true, Options: []*wire.PollOption{{Id: 0, Text: "yes"}}, Result: &wire.PollResult{RecentVoters: []int64{12345678999}, VotersCount: 1, OptionResults: []*wire.PollOptionResult{{OptionId: 0, VotesCount: 1}}, ChosenOptionIds: []int64{0}}}}
	if e := validateMessage(m); e != nil {
		t.Fatal(e)
	}
	raw := messagePayload(m)
	if strings.Contains(string(raw), "12345678999") || !strings.Contains(string(raw), `"poll_id":"9223372036854775807"`) {
		t.Fatalf("poll normalization %s", raw)
	}
	m.Poll.Result.VotersCount = -1
	if validateMessage(m) == nil {
		t.Fatal("negative vote count accepted")
	}
}

func TestIncomingExPeerChannelDoesNotChangeIntrinsicMessageIdentity(t *testing.T) {
	m := &wire.UpdateMessage{Peer: &wire.Peer{Type: 2, Id: 99}, ExPeer: &wire.Peer{Type: 3, Id: 99}, SenderId: 42, Rid: 55, Date: 1720000000000, Message: &wire.Message{Text: &wire.TextMessage{Text: "channel"}}}
	a, e := decodeEvents("12345", marshal(t, &wire.UpdateContainer{Message: m}))
	if e != nil || a[0].Peer.Type != "channel" {
		t.Fatalf("ExPeer not respected %v %v", a, e)
	}
	m.ExPeer = nil
	b, e := decodeEvents("12345", marshal(t, &wire.UpdateContainer{Message: m}))
	if e != nil || a[0].ID != b[0].ID {
		t.Fatal("legacy and ExPeer update identities diverged")
	}
	m.ExPeer = &wire.Peer{Type: 3, Id: 98}
	if _, e = decodeEvents("12345", marshal(t, &wire.UpdateContainer{Message: m})); e == nil {
		t.Fatal("mismatched ExPeer account accepted")
	}
}
