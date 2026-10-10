package balemeow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestRichMessagesUseOfficialJSONAndKeyboardWrappers(t *testing.T) {
	var requests atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		requests.Add(1)
		q := &wire.SendMessageRequest{}
		if r.Method != "SendMessage" || decode(r.Payload, q) != nil || q.Rid != 9007199254740993 || q.Peer.Type != 2 || q.ExPeer.Type != 3 || q.ExPeer.AccessHash != 99 || !q.IsSilent || q.GetQuotedMessage().GetRid() != -9007199254740993 {
			t.Error("rich send lost journal RID, quote, silence or channel ExPeer")
		}
		field, _, _ := protowire.ConsumeTag(marshal(t, q.Message))
		if q.Message.Json != nil {
			if field != 7 {
				t.Error("JSON message must use union field7")
			}
			var v struct {
				DataType string                     `json:"dataType"`
				Data     map[string]json.RawMessage `json:"data"`
			}
			if json.Unmarshal([]byte(q.Message.Json.RawJson), &v) != nil {
				t.Error("malformed JSON message")
			}
			if v.DataType == "contact" {
				var contact struct {
					Name   string
					Phones []string
					Emails []string
					Photo  string
				}
				if json.Unmarshal(v.Data["contact"], &contact) != nil || contact.Name != "دوست‌من" || len(contact.Phones) != 1 || contact.Phones[0] != "989121234567" || len(contact.Emails) != 1 || contact.Photo != "" {
					t.Error("contact projection differs from official shape")
				}
			} else if v.DataType == "location" {
				if string(v.Data["location"]) != `{"latitude":0,"longitude":180}` {
					t.Errorf("zero coordinate changed: %s", v.Data["location"])
				}
			} else {
				t.Error("unexpected raw JSON type")
			}
		} else {
			if field != 13 || q.Message.Template == nil || q.Message.Template.Id != 0 || q.Message.Template.GetMessage().GetText().GetText() != "سلام‌دوست" {
				t.Error("template structure differs from native schema")
			}
			x := q.Message.Template
			switch {
			case x.InlineKeyboard != nil:
				b := x.InlineKeyboard.Rows[0].Buttons
				if len(b) != 4 || b[0].GetUrl().GetValue() != "https://example.invalid" || b[1].GetCallbackData().GetValue() != "data" || b[2].GetCopyText().GetText() != "copy" || b[3].GetWebApp().GetUrl() != "https://example.invalid/app" {
					t.Error("inline wrappers mismatch")
				}
			case x.ReplyKeyboard != nil:
				b := x.ReplyKeyboard.Rows[0].Buttons
				if len(b) != 4 || !b[0].GetRequestContact().GetValue() || !b[1].GetRequestLocation().GetValue() || b[2].GetWebApp().GetUrl() != "https://example.invalid/app" || b[3].Text != "plain" {
					t.Error("reply keyboard mismatch")
				}
			case x.RemoveKeyboard != nil:
				if !x.RemoveKeyboard.RemoveKeyboard || x.RemoveKeyboard.Selective == nil || x.RemoveKeyboard.Selective.Value {
					t.Error("explicit false selective must stay present")
				}
			default:
				t.Error("missing keyboard")
			}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.SendMessageResponse{Date: 1720000000000})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("channel", &wire.PeerRef{Id: 77, AccessHash: 99})
	base := `"peer":{"type":"channel","id":"77"},"request_id":"9007199254740993","reply_message_id":"-9007199254740993","silent":true,`
	for _, tc := range []struct{ op, body string }{
		{"send.contact", `"name":"دوست‌من","phones":["09121234567"],"emails":["test@example.invalid"]`},
		{"send.location", `"latitude":0,"longitude":180`},
		{"send.template", `"text":"سلام‌دوست","inline_keyboard":[[{"text":"URL","url":"https://example.invalid"},{"text":"callback","callback_data":"data"},{"text":"copy","copy_text":"copy"},{"text":"app","web_app_url":"https://example.invalid/app"}]]`},
		{"send.template", `"text":"سلام‌دوست","reply_keyboard":[[{"text":"contact","request_contact":true},{"text":"location","request_location":true},{"text":"app","web_app_url":"https://example.invalid/app"},{"text":"plain"}]]`},
		{"send.template", `"text":"سلام‌دوست","remove_keyboard":true,"selective":false`},
	} {
		raw, err := c.richMessageCall(context.Background(), tc.op, json.RawMessage(`{`+base+tc.body+`}`))
		if err != nil || !strings.Contains(string(raw), `"message_id":"9007199254740993"`) || strings.Contains(string(raw), "access_hash") {
			t.Fatalf("%s %v", raw, err)
		}
	}
	if requests.Load() != 5 {
		t.Fatal("unexpected additional provider work")
	}
}

func TestRichMessageValidationBeforeProviderWork(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body string }{
		{"send.contact", `"name":"N","phones":[]`},
		{"send.contact", `"name":"N","phones":["09121234567","+989121234567"]`},
		{"send.contact", `"name":"N","phones":["09121234567"],"emails":["Name <test@example.invalid>"]`},
		{"send.location", `"longitude":0`},
		{"send.location", `"latitude":-91,"longitude":0`},
		{"send.location", `"latitude":0,"longitude":181`},
		{"send.template", `"text":"x","inline_keyboard":[]`},
		{"send.template", `"text":"x","inline_keyboard":[[{"text":"x","callback_data":"a","url":"https://example.invalid"}]]`},
		{"send.template", `"text":"x","inline_keyboard":[[{"text":"x","url":"javascript:alert(1)"}]]`},
		{"send.template", `"text":"x","inline_keyboard":[[{"text":"x","url":"https://user:pass@example.invalid"}]]`},
		{"send.template", `"text":"x","reply_keyboard":[[{"text":"x","request_contact":true,"request_location":true}]]`},
		{"send.template", `"text":"x","reply_keyboard":[[{"text":"x","request_contact":false}]]`},
		{"send.template", `"text":"x","reply_keyboard":[[{"text":"x","web_app_url":"http://example.invalid"}]]`},
		{"send.template", `"text":"x","remove_keyboard":false`},
		{"send.template", `"text":"x","remove_keyboard":true,"reply_keyboard":[[{"text":"x"}]]`},
		{"send.template", `"text":"x","remove_keyboard":true,"json":"arbitrary"`},
	} {
		_, err := c.richMessageCall(context.Background(), tc.op, json.RawMessage(`{"request_id":"1",`+tc.body+`}`))
		if codeOf(err) != "INVALID_REQUEST" {
			t.Fatalf("%s: %v", tc.body, err)
		}
	}
	_, err := c.richMessageCall(context.Background(), "send.location", json.RawMessage(`{"latitude":0,"longitude":0}`))
	if codeOf(err) != "INVALID_REQUEST_ID" {
		t.Fatal(err)
	}
}

func TestRichSendLostAcknowledgementIsUnknownAndNeverRetried(t *testing.T) {
	var sends atomic.Int32
	fake := newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request != nil {
			sends.Add(1)
			_ = ws.CloseNow()
		}
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	_, err := c.richMessageCall(context.Background(), "send.location", json.RawMessage(`{"request_id":"1","peer":{"type":"user","id":"12345"},"latitude":0,"longitude":0}`))
	var e *domains.Error
	if !errors.As(err, &e) || !e.Ambiguous || e.Retryable || sends.Load() != 1 {
		t.Fatalf("%v calls=%d", err, sends.Load())
	}
}
