package eitaameow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func historyMessage(id, date int, user int) object {
	return object{"_": "message", "id": id, "date": date, "peer_id": object{"_": "peerUser", "user_id": user}, "message": "synthetic"}
}

func TestSmallHistoryPagesUseReviewedBoundedReadAndActualOutputCursor(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method != "messages.getHistory" || p.num("limit") != 50 {
			t.Fatal("small unstable provider read used")
		}
		return object{"_": "messages.messages", "messages": []object{historyMessage(9, 100, 43), historyMessage(7, 100, 43), historyMessage(8, 100, 43), historyMessage(6, 100, 43)}, "chats": []object{}, "users": []object{}}, 200
	})
	c.session.Token, c.session.UserID = "synthetic", "42"
	peer := domains.Peer{Type: "user", ID: "43"}
	raw, err := c.history(context.Background(), callRequest{Peer: peer, Limit: 2}, object{"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(2)})
	out := pageResult(t, raw, err)
	cur, err := c.decodeCursor(out.str("next_cursor"), "history", peer.Key())
	if err != nil || cur.ID != 8 || len(asObjects(out["items"])) != 2 || out["has_more"] != true {
		t.Fatal("local truncation skipped the omitted rows")
	}
}

func TestLargeHistoryLimitContinuesPastObservedFiftyRowProviderCap(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls++
		if method != "messages.getHistory" || p.num("limit") != 50 {
			t.Fatal("unreviewed large provider page used")
		}
		rows := []object{}
		if calls == 1 {
			for id := 75; id >= 26; id-- {
				rows = append(rows, historyMessage(id, 100, 43))
			}
		} else {
			if p.num("offset_id") != 26 {
				t.Fatal("continuation skipped the actual last delivered message")
			}
			rows = append(rows, historyMessage(26, 100, 43), historyMessage(25, 100, 43))
		}
		return object{"_": "messages.messagesSlice", "count": 1000, "messages": rows, "chats": []object{}, "users": []object{}}, 200
	})
	c.session.Token, c.session.UserID = "synthetic", "42"
	peer := domains.Peer{Type: "user", ID: "43"}
	request := callRequest{Peer: peer, Limit: 100}
	input := object{"_": "inputPeerUser", "user_id": int64(43), "access_hash": int64(2)}
	raw, err := c.history(context.Background(), request, input)
	first := pageResult(t, raw, err)
	if len(asObjects(first["items"])) != 50 || first["has_more"] != true {
		t.Fatal("provider cap was mistaken for the end of history")
	}
	request.Cursor = first.str("next_cursor")
	raw, err = c.history(context.Background(), request, input)
	last := pageResult(t, raw, err)
	if len(asObjects(last["items"])) != 1 || asObjects(last["items"])[0].str("id") != "25" || last["has_more"] != false || last["complete"] != false || calls != 2 {
		t.Fatal("tail or overlapping boundary was lost, or placeholder count was trusted")
	}
}

func TestHistoryMigrationNoticeDoesNotPanicOrInventMessageIdentity(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, _ object) (object, int) {
		if method != "messages.getHistory" {
			t.Fatal("unexpected method")
		}
		return object{"_": "messages.messages", "messages": []object{migrationNotice()}, "chats": []object{}, "users": []object{}}, 200
	})
	c.session.Token, c.session.UserID = "synthetic", "42"
	raw, err := c.history(context.Background(), callRequest{Peer: domains.Peer{Type: "channel", ID: "91"}, Limit: 2}, object{"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(2)})
	out := pageResult(t, raw, err)
	if len(asObjects(out["items"])) != 0 || out["omitted_service_notices"] != float64(1) || out["complete"] != false {
		t.Fatalf("migration notice claimed as a message: %s", raw)
	}
	if _, err := c.projectReadEvent(context.Background(), migrationNotice(), domains.Peer{Type: "channel", ID: "91"}); err == nil {
		t.Fatal("nonmessage accepted as exact message read")
	}
}
func pageResult(t *testing.T, raw json.RawMessage, err error) object {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var out object
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return jsonObject(out).(object)
}
func jsonObject(v any) any {
	switch o := v.(type) {
	case object:
		for k, x := range o {
			o[k] = jsonObject(x)
		}
		return o
	case map[string]any:
		out := object{}
		for k, x := range o {
			out[k] = jsonObject(x)
		}
		return out
	case []any:
		for i, x := range o {
			o[i] = jsonObject(x)
		}
		return o
	default:
		return v
	}
}
func TestDialogsUseActualRowsAndAccountBoundOffsetPeer(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls++
		if method != "messages.getDialogs" {
			t.Error(method)
		}
		if calls == 1 && asObject(p["offset_peer"]).str("_") != "inputPeerEmpty" {
			t.Error("wrong initial offset")
		}
		if calls == 2 && (p.num("offset_id") != 10 || p.num("offset_date") != 100 || asObject(p["offset_peer"]).num("user_id") != 43 || p["exclude_pinned"] != true) {
			t.Errorf("lost offset metadata: %#v", p)
		}
		return object{"_": "messages.dialogsSlice", "count": 3, "dialogs": []object{{"_": "dialog", "peer": object{"_": "peerUser", "user_id": 43}, "top_message": 10, "notify_settings": object{"_": "peerNotifySettings"}, "read_inbox_max_id": 0, "read_outbox_max_id": 0, "unread_count": 0, "unread_mentions_count": 0, "unread_reactions_count": 0}}, "messages": []object{historyMessage(10, 100, 43)}, "chats": []object{}, "users": []object{{"_": "user", "id": 43, "access_hash": 555, "first_name": "Visible"}, {"_": "user", "id": 44, "access_hash": 666, "first_name": "Metadata only"}}}, 200
	})
	mediaClientSource(c, nil)
	c.cfg.ConnectionID = "connection-a"
	raw, err := c.dialogs(context.Background(), callRequest{Limit: 1}, "")
	first := pageResult(t, raw, err)
	rows := asObjects(first["items"])
	if len(rows) != 1 || asObject(rows[0]["peer"]).str("id") != "43" || first["has_more"] != true || first["complete"] != false {
		t.Fatalf("incorrect dialog projection: %s", raw)
	}
	raw, err = c.dialogs(context.Background(), callRequest{Limit: 1, Cursor: first.str("next_cursor")}, "")
	second := pageResult(t, raw, err)
	if second["has_more"] != false || second.str("pagination_stop_reason") != "non_advancing_cursor" {
		t.Fatalf("repeating page continued: %s", raw)
	}
	for _, change := range []string{"tamper", "connection", "operation"} {
		t.Run(change, func(t *testing.T) {
			cursor := first.str("next_cursor")
			kind := ""
			c.cfg.ConnectionID = "connection-a"
			if change == "tamper" {
				cursor += "x"
			}
			if change == "connection" {
				c.cfg.ConnectionID = "connection-b"
			}
			if change == "operation" {
				kind = "group"
			}
			before := calls
			_, err := c.dialogs(context.Background(), callRequest{Cursor: cursor}, kind)
			var de *domains.Error
			if !errors.As(err, &de) || de.Code != "INVALID_CURSOR" || calls != before {
				t.Fatalf("unsafe cursor: %v", err)
			}
		})
	}
}
func TestFilteredDialogsStillAdvanceAcrossOtherPeerTypes(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		return object{"_": "messages.dialogsSlice", "count": 2, "dialogs": []object{{"_": "dialog", "peer": object{"_": "peerUser", "user_id": 43}, "top_message": 10, "notify_settings": object{"_": "peerNotifySettings"}, "read_inbox_max_id": 0, "read_outbox_max_id": 0, "unread_count": 0, "unread_mentions_count": 0, "unread_reactions_count": 0}}, "messages": []object{historyMessage(10, 100, 43)}, "chats": []object{}, "users": []object{{"_": "user", "id": 43, "access_hash": 555}}}, 200
	})
	mediaClientSource(c, nil)
	raw, err := c.dialogs(context.Background(), callRequest{Limit: 1}, "group")
	out := pageResult(t, raw, err)
	if len(asObjects(out["items"])) != 0 || out["has_more"] != true || out.str("next_cursor") == "" {
		t.Fatalf("filter ended provider pagination: %s", raw)
	}
}
func TestHistoryCursorUsesMessageIDsAtEqualTimestamps(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls++
		id := 10
		if calls > 1 {
			if p.num("offset_id") != 10 || p.num("offset_date") != 100 {
				t.Error("lost history offset")
			}
			id = 9
		}
		return object{"_": "messages.messages", "messages": []object{historyMessage(id, 100, 42)}, "chats": []object{}, "users": []object{}}, 200
	})
	mediaClientSource(c, nil)
	c.cfg.ConnectionID = "connection-a"
	p := callRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Limit: 1}
	raw, err := c.history(context.Background(), p, object{"_": "inputPeerSelf"})
	first := pageResult(t, raw, err)
	p.Cursor = first.str("next_cursor")
	raw, err = c.history(context.Background(), p, object{"_": "inputPeerSelf"})
	second := pageResult(t, raw, err)
	if second["has_more"] != true || first.str("next_cursor") == second.str("next_cursor") {
		t.Fatalf("equal dates did not advance IDs: %s", raw)
	}
	p.Peer.ID = "43"
	before := calls
	_, err = c.history(context.Background(), p, object{"_": "inputPeerSelf"})
	if err == nil || calls != before {
		t.Fatal("cursor crossed peer")
	}
}
func TestHistoryDownloadRequiresDurableReference(t *testing.T) {
	m := historyMessage(10, 100, 42)
	m["media"] = object{"_": "messageMediaPhoto", "photo": asObject(avatarFull(syntheticPNG(t))["profile_photo"])}
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		return object{"_": "messages.messages", "messages": []object{m}, "chats": []object{}, "users": []object{}}, 200
	})
	mediaClientSource(c, nil)
	p := callRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Limit: 1}
	input := object{"_": "inputPeerSelf"}
	read := func() bool {
		t.Helper()
		raw, err := c.history(context.Background(), p, input)
		out := pageResult(t, raw, err)
		return asObject(asObjects(out["items"])[0]["media"])["download_supported"] == true
	}
	if read() {
		t.Fatal("unregistered media advertised")
	}
	saved := false
	c.cfg.SaveMediaReference = func(_ context.Context, peer domains.Peer, id string, m domains.ProviderMedia) (bool, error) {
		if peer.Key() != "user:42" || id != "10" || m.Provider != domains.ProviderEitaa || len(m.Data) == 0 {
			t.Error("wrong reference scope")
		}
		saved = true
		return true, nil
	}
	if !read() || !saved {
		t.Fatal("registered reference unavailable")
	}
	c.cfg.SaveMediaReference = func(context.Context, domains.Peer, string, domains.ProviderMedia) (bool, error) {
		return false, errors.New("synthetic storage failure")
	}
	if _, err := c.history(context.Background(), p, input); err == nil {
		t.Fatal("storage failure became success")
	}
}

func TestDialogCursorStopsAlternatingBoundaryCycle(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(string, object) (object, int) {
		calls++
		user := 43
		if calls%2 == 0 {
			user = 44
		}
		return object{"_": "messages.dialogsSlice", "count": 3, "dialogs": []object{{"_": "dialog", "peer": object{"_": "peerUser", "user_id": user}, "top_message": 10, "read_inbox_max_id": 0, "read_outbox_max_id": 0, "unread_count": 0, "unread_mentions_count": 0, "notify_settings": object{"_": "peerNotifySettings"}}}, "messages": []object{historyMessage(10, 100, user)}, "chats": []object{}, "users": []object{{"_": "user", "id": user, "access_hash": 555}}}, 200
	})
	mediaClientSource(c, nil)
	cursor := ""
	for page := 0; page < 3; page++ {
		raw, err := c.dialogs(context.Background(), callRequest{Limit: 1, Cursor: cursor}, "")
		out := pageResult(t, raw, err)
		if page < 2 {
			if out["has_more"] != true {
				t.Fatalf("first traversal did not advance: %s", raw)
			}
			cursor = out.str("next_cursor")
		} else if out["has_more"] != false || out.str("pagination_stop_reason") != "non_advancing_cursor" {
			t.Fatalf("cycle continued: %s", raw)
		}
	}
}
