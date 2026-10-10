package balemeow

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
)

func TestStoryReportUsesWrappedIDsAtField103(t *testing.T) {
	// Independent bytes from the official web encoder layout: Request.report=1,
	// Report.kind=1, Report.storyReport=103, StoryReport.storyId=1 repeated
	// google.protobuf.StringValue wrappers, not repeated bare strings.
	want, _ := hex.DecodeString("0a0f0805ba060a0a030a01610a030a0162")
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		if r.Service != "bale.report.v1.Report" || r.Method != "ReportInappropriateContent" || !bytes.Equal(r.Payload, want) {
			t.Errorf("wrong report layout: %s %s %x", r.Service, r.Method, r.Payload)
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.Empty{})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	raw, err := c.Call(context.Background(), "report.story", json.RawMessage(`{"story_ids":["a","b"],"kind":5,"request_id":"9007199254740993"}`))
	if err != nil || string(raw) != `{"acknowledged":true}` {
		t.Fatalf("%s %v", raw, err)
	}
}
