package balemeow

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func TestMiniAppOfficialRequestShapesAndNoUnsignedFallback(t *testing.T) {
	var fake *fakeWS
	var calls atomic.Int32
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		calls.Add(1)
		var out proto.Message
		switch r.Method {
		case "GetMiniAppUrl":
			p := &wire.MiniAppURLRequest{}
			if decode(r.Payload, p) != nil || p.BotUserId != 42 || p.ScreenMode != 2 || p.DirectLink == nil || p.Main != nil || p.DirectLink.ShortName != "support" || p.DirectLink.StartParam.GetValue() != "hello" || p.Theme.GetBgColor().GetValue() != "#112233" {
				t.Errorf("incorrect launch request %v", p)
			}
			out = &wire.MiniAppURLResponse{Url: "https://example.invalid/app#signed-data", ScreenMode: 2, QueryId: &wire.StringValue{Value: "provider-query"}}
		case "GetWebappHash":
			p := &wire.MiniAppHashRequest{}
			if decode(r.Payload, p) != nil || p.Data != "payload" {
				t.Error("incorrect hash request")
			}
			// No provider signature means an error, never an unsigned generated fallback.
			out = &wire.MiniAppHashResponse{QueryId: "q", AuthDate: 1720000000}
		case "InvokeCustomMethod":
			p := &wire.MiniAppCustomRequest{}
			if decode(r.Payload, p) != nil || p.Method != "example" || p.Params != `{"a":1}` {
				t.Error("incorrect app request")
			}
			out = &wire.MiniAppCustomResponse{Data: `{"ok":true}`}
		case "SendInlineCallback":
			p := &wire.MiniAppCallbackRequest{}
			if decode(r.Payload, p) != nil || p.MessageId.GetRid() != -9223372036854775808 || p.MessageId.GetDate() != 1720000000000 || p.Peer.Type != 1 || p.Data.GetValue() != "button" {
				t.Error("callback lost position/data")
			}
			out = &wire.Empty{}
		case "GetLinkSummary":
			p := &wire.LinkSummaryRequest{}
			if decode(r.Payload, p) != nil || p.Url != "https://example.invalid/article" {
				t.Error("summary URL lost")
			}
			out = &wire.LinkSummaryResponse{Summary: "Synthetic summary"}
		default:
			t.Errorf("unexpected RPC %s", r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, out)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	cases := []struct{ op, body, want string }{
		{"miniapp.url", `{"bot_id":"42","screen_mode":2,"source":"direct","short_name":"support","start_param":"hello","theme":{"bg_color":"#112233"}}`, `"query_id":"provider-query"`},
		{"miniapp.custom", `{"bot_id":"42","method":"example","params":"{\"a\":1}","request_id":"23"}`, `"acknowledged":true`},
		{"bot.callback", `{"peer":{"type":"user","id":"42"},"message_id":"-9223372036854775808","date_ms":"1720000000000","data":"button","request_id":"24"}`, `"acknowledged":true`},
		{"link.summary", `{"url":"https://example.invalid/article"}`, `"summary":"Synthetic summary"`},
	}
	for _, tc := range cases {
		out, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
		if err != nil || !strings.Contains(string(out), tc.want) {
			t.Fatalf("%s: %s %v", tc.op, out, err)
		}
	}
	_, err := c.Call(context.Background(), "miniapp.hash", json.RawMessage(`{"bot_id":"42","data":"payload"}`))
	if err == nil {
		t.Fatal("invented unsigned launch data")
	}
	before := calls.Load()
	for _, body := range []string{`{"bot_id":"42","source":"direct"}`, `{"bot_id":"42","source":"keyboard","url":"file:///private"}`, `{"bot_id":"4294967296"}`, `{"bot_id":"42","theme":{"unknown":"#112233"}}`} {
		if _, err := c.Call(context.Background(), "miniapp.url", json.RawMessage(body)); err == nil {
			t.Error("invalid launch accepted", body)
		}
	}
	if calls.Load() != before {
		t.Error("invalid launch reached network")
	}
}
