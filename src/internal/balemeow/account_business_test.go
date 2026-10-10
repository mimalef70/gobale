package balemeow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
)

func TestReportOperationsUseReviewedUnionAndChannelExPeer(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		if r.Service != "bale.report.v1.Report" {
			t.Error("wrong service")
		}
		if r.Method == "ReportDismiss" {
			v := &wire.AccountReportDismissRequest{}
			if decode(r.Payload, v) != nil || v.Peer.Type != 3 || v.Peer.Id != 77 || v.Peer.AccessHash != 99 {
				t.Error("dismiss must encode ExPeer at field2")
			}
		} else {
			v := &wire.AccountReportRequest{}
			if decode(r.Payload, v) != nil || v.Report == nil || v.Report.Kind != 5 {
				t.Error("invalid report wrapper")
			}
			if v.Report.PeerReport != nil {
				if v.Report.PeerReport.Peer.Type != 3 || v.Report.PeerReport.Source != 1 {
					t.Error("wrong peer report")
				}
			} else {
				if v.Report.MessageReport == nil || v.Report.MessageReport.Peer.Type != 3 || len(v.Report.MessageReport.Mids) != 1 || v.Report.MessageReport.Mids[0].Rid != -9007199254740993 {
					t.Error("wrong message report")
				}
			}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.Empty{})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("channel", &wire.PeerRef{Id: 77, AccessHash: 99})
	for _, tc := range []struct{ op, extra string }{{"report.peer", `,"kind":5`}, {"report.messages", `,"kind":5,"messages":[{"message_id":"-9007199254740993","date_ms":"1720000000000"}]`}, {"report.dismiss", ""}} {
		v, e := c.accountBusinessCall(context.Background(), tc.op, json.RawMessage(`{"request_id":"1","peer":{"type":"channel","id":"77"}`+tc.extra+`}`))
		if e != nil || string(v) != `{"acknowledged":true}` {
			t.Fatalf("%s %v", v, e)
		}
	}
	c = New(Options{})
	_, err := c.accountBusinessCall(context.Background(), "report.peer", json.RawMessage(`{"request_id":"1","kind":7}`))
	if codeOf(err) != "INVALID_REQUEST" {
		t.Fatal(err)
	}
}
