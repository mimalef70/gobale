package balemeow

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"testing"
	"time"
)

// Unicode formatting characters are message content. Never interpret user
// literal backslash sequences as a second JSON layer.
func TestTextPreservesZWNJAndLiteralEscapesThroughWireAndJSON(t *testing.T) {
	text := "متن زمان\u200cبندی\u200cشده؛ literal \\u200c \\n \\\\ \"quoted\"\nactual newline ✅"
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, msg *wire.ClientMessage) {
		if msg.Request == nil {
			return
		}
		r := msg.Request
		if r.Method != "SendMessage" {
			t.Fatalf("unexpected method %s", r.Method)
		}
		q := &wire.SendMessageRequest{}
		if err := decode(r.Payload, q); err != nil {
			t.Fatal(err)
		}
		if q.GetMessage().GetText().GetText() != text {
			t.Errorf("wire altered text: got %q want %q", q.GetMessage().GetText().GetText(), text)
		}
		// Echo the exact provider string, as a normal authenticated update would.
		fake.send(ws, &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Update: marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 1, Id: 42}, SenderId: 12345, Rid: 77, Date: 1720000000000, Message: q.Message}})})}})
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.SendMessageResponse{Date: 1720000000000})}})
	})
	received := make(chan domains.Event, 1)
	c := fake.client()
	connectTest(t, c, func(_ context.Context, e domains.Event) error { received <- e; return nil })
	_, err := c.Send(context.Background(), domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Text: text, RequestID: "77"})
	if err != nil {
		t.Fatal(err)
	}
	var event domains.Event
	select {
	case event = <-received:
	case <-time.After(time.Second):
		t.Fatal("echo update did not arrive")
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message != text {
		t.Fatalf("JSON payload altered text: got %q want %q", payload.Message, text)
	}
	for _, original := range []string{"الف\u200cب", `الف\u200cب`, "line\nnext", `line\nnext`} {
		raw := messagePayload(&wire.Message{Text: &wire.TextMessage{Text: original}})
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Message != original {
			t.Fatalf("text normalization corrupted %q into %q", original, payload.Message)
		}
	}
}
