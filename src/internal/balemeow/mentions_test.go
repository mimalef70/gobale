package balemeow

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMentionLiteralWireAndNativeSend(t *testing.T) {
	// Literal field 2 is packed uint32. This expectation is independent of the
	// generated encoder and includes a multi-byte varint boundary.
	expected, err := hex.DecodeString("0a01781203018001")
	require.NoError(t, err)
	got, err := proto.Marshal(mentionedText("x", []string{"1", "128"}))
	require.NoError(t, err)
	require.Equal(t, expected, got)
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		request := &wire.SendMessageRequest{}
		require.NoError(t, proto.Unmarshal(m.Request.Payload, request))
		require.Equal(t, []uint32{1, 128}, request.Message.Text.Mentions)
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.SendMessageResponse{Date: 1720000000000})}})
	})
	client := fake.client()
	connectTest(t, client, acceptingSink)
	_, err = client.Send(context.Background(), domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "77"}, Text: "x", Mentions: []string{"1", "128"}, RequestID: "123"})
	require.NoError(t, err)
}
