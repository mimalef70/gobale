package balemeow

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
)

func receivedMessage(t *testing.T, message *wire.Message, quote *wire.QuotedMessage) domains.Event {
	t.Helper()
	events, err := decodeEvents("12345", marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{
		Peer: &wire.Peer{Type: 2, Id: 77}, SenderId: 42, Date: 1720000000001, Rid: -9007199254740993, Message: message, QuotedMessage: quote,
	}}))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Message)
	return events[0]
}

func TestMessageProjectionReplyForwardAndUnknownEdit(t *testing.T) {
	text := "سلام دوست ✅ literal \\n"
	quote := &wire.QuotedMessage{SenderUserId: 43, MessageId: &wire.Int64Value{Value: -33}, Date: 1720000000000,
		Peer: &wire.Peer{Type: 1, Id: 43, AccessHash: 9123456789}, Message: &wire.Message{Text: &wire.TextMessage{Text: text}}}
	reply := receivedMessage(t, &wire.Message{Text: &wire.TextMessage{Text: "answer"}}, quote)
	require.Equal(t, "-9007199254740993", reply.Message.ID)
	require.Equal(t, "77", reply.Message.ChatID)
	require.Equal(t, "42", reply.Message.From)
	require.Equal(t, "unavailable", reply.Message.SenderNameStatus)
	require.NotNil(t, reply.Message.IsFromMe)
	require.False(t, *reply.Message.IsFromMe)
	require.Equal(t, reply.Time, reply.Message.Timestamp)
	require.Equal(t, "answer", reply.Message.Body)
	require.Equal(t, "-33", reply.Message.RepliedToID)
	require.Equal(t, text, reply.Message.QuotedBody)
	require.Nil(t, reply.Message.ForwardedFrom)
	forward := receivedMessage(t, &wire.Message{Empty: &wire.Empty{}}, quote)
	require.Equal(t, "text", forward.Message.Kind)
	require.Equal(t, text, forward.Message.Body)
	require.Empty(t, forward.Message.RepliedToID)
	require.Empty(t, forward.Message.QuotedBody)
	require.Equal(t, "-33", forward.Message.ForwardedFrom.MessageID)
	require.Equal(t, "43", forward.Message.ForwardedFrom.SenderID)
	require.Equal(t, &domains.Peer{Type: "user", ID: "43"}, forward.Message.ForwardedFrom.Peer)
	encoded, err := json.Marshal(forward)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "9123456789")
	require.NotContains(t, string(encoded), "access_hash")
	require.NotContains(t, string(encoded), "quoted_message")
	// Literal wrapped edit date and absent updater: never infer sender from peer.
	literal, err := hex.DecodeString("920a140a040801102a10071a057a030a0178220308e807")
	require.NoError(t, err)
	edits, err := decodeEvents("7", literal)
	require.NoError(t, err)
	require.Equal(t, "7", edits[0].Message.OriginalMessageID)
	require.Nil(t, edits[0].Message.IsFromMe)
	require.Empty(t, edits[0].Message.From)
	known, err := decodeEvents("7", marshal(t, &wire.UpdateContainer{Edited: &wire.UpdateMessageEdited{
		Peer: &wire.Peer{Type: 2, Id: 42}, Rid: 9, Date: &wire.Int64Value{Value: 1720000000000},
		UpdaterUserId: &wire.Int32Value{Value: 7}, Message: &wire.Message{Text: &wire.TextMessage{Text: "edited"}},
	}}))
	require.NoError(t, err)
	client := New(Options{})
	client.rememberUserName(&wire.User{Id: 7, Name: "Editor"})
	edited := client.prepareEvent(context.Background(), known[0])
	require.Equal(t, "7", edited.Message.EditorID)
	require.Equal(t, "7", edited.SenderID, "event actor retains actual updater provenance")
	require.Empty(t, edited.Message.From)
	require.Nil(t, edited.Message.IsFromMe)
	require.Empty(t, edited.Message.SenderDisplayName)
	require.Equal(t, "unavailable", edited.Message.SenderNameStatus)
}

func TestMessageProjectionSpecialBodiesAndAttachmentMetadata(t *testing.T) {
	poll := receivedMessage(t, &wire.Message{Poll: &wire.PollMessage{PollId: -99, Question: "گزینه؟", Options: []*wire.PollOption{{Id: 0, Text: "اول"}, {Id: 1, Text: "دوم"}}, IsAnonymous: true}}, nil)
	require.Equal(t, "گزینه؟\n1. اول\n2. دوم", poll.Message.Body)
	require.True(t, poll.Message.Supported)
	unknown := receivedMessage(t, &wire.Message{}, nil)
	require.Equal(t, "[Unsupported message]", unknown.Message.Body)
	require.Equal(t, "unsupported", unknown.Message.Kind)
	require.False(t, unknown.Message.Supported)
	for _, kind := range []string{"voice", "audio", "file"} {
		t.Run(kind, func(t *testing.T) {
			ext := &wire.DocumentExt{}
			if kind == "voice" {
				ext.Voice = &wire.Voice{Duration: 1200}
			} else if kind == "audio" {
				ext.Audio = &wire.Audio{Duration: 1200}
			}
			doc := &wire.Message{Document: &wire.DocumentMessage{FileId: -90123456789, AccessHash: 9876543210, FileSize: 17, Name: "synthetic.ogg", MimeType: "audio/ogg", Ext: ext, Caption: &wire.TextMessage{Text: "caption"}}}
			quote := &wire.QuotedMessage{SenderUserId: 43, Date: 1720000000000, MessageId: &wire.Int64Value{Value: -7}, Message: doc}
			forward := receivedMessage(t, &wire.Message{Empty: &wire.Empty{}}, quote)
			require.Equal(t, "caption", forward.Message.Body)
			require.Equal(t, kind, forward.Message.Media.Type)
			require.Equal(t, "-90123456789", forward.Message.Media.FileID)
			require.Equal(t, int64(17), forward.Message.Media.Size)
			require.Equal(t, "synthetic.ogg", forward.Message.Media.Name)
			require.Equal(t, "audio/ogg", forward.Message.Media.MIMEType)
			require.False(t, forward.Message.Media.DownloadSupported)
			require.NotNil(t, forward.Media)
			client := New(Options{SaveMediaReference: func(context.Context, domains.Peer, string, domains.ProviderMedia) (bool, error) { return true, nil }})
			prepared := client.prepareEvent(context.Background(), forward)
			require.False(t, prepared.Message.Media.DownloadSupported, "the durable sink has not accepted the reference yet")
			// A failed or unavailable later download cannot erase the descriptor.
			withoutStore := New(Options{}).prepareEvent(context.Background(), forward)
			require.False(t, withoutStore.Message.Media.DownloadSupported)
			require.Equal(t, prepared.Message.Media.FileID, withoutStore.Message.Media.FileID)
			reply := receivedMessage(t, &wire.Message{Text: &wire.TextMessage{Text: "reply"}}, quote)
			require.Nil(t, reply.Media, "reply quotation must never rebind the original attachment")
			require.Nil(t, reply.Message.Media)
			nested := receivedMessage(t, &wire.Message{Template: &wire.TemplateMessage{Message: doc}}, nil)
			require.Equal(t, "caption", nested.Message.Body)
			require.Equal(t, kind, nested.Message.Media.Type)
		})
	}
}

func TestColdIncomingSenderNameResolvedInsideGateway(t *testing.T) {
	var requests atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
		if message.Request == nil {
			return
		}
		require.Equal(t, "GetContacts", message.Request.Method)
		requests.Add(1)
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: message.Request.Index, Payload: marshal(t, &wire.ContactsResponse{Users: []*wire.User{{Id: 42, AccessHash: 99887766, Name: "Public name", LocalName: &wire.StringValue{Value: "نام مخاطب"}}}})}})
	})
	client := fake.client()
	client.senderLookupAfter = time.Time{}
	client.senderContactsAfter = time.Time{}
	accepted := make(chan domains.Event, 2)
	connectTest(t, client, func(_ context.Context, event domains.Event) error { accepted <- event; return nil })
	send := func(rid int64) {
		fake.connections.Range(func(connection, _ any) bool {
			fake.send(connection.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Update: marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 1, Id: 42}, SenderId: 42, Rid: rid, Date: 1720000000000, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}}})})}})
			return true
		})
	}
	for _, rid := range []int64{1, 2} {
		send(rid)
		select {
		case event := <-accepted:
			require.Equal(t, "نام مخاطب", event.Message.SenderDisplayName)
			require.Equal(t, "available", event.Message.SenderNameStatus)
		case <-time.After(time.Second):
			t.Fatal("name enrichment blocked event delivery or websocket response routing")
		}
	}
	require.Equal(t, int32(1), requests.Load(), "warm names require no supplemental consumer requests")
	other := New(Options{})
	name, status := other.cachedSenderDisplayName("42")
	require.Empty(t, name)
	require.Equal(t, "unavailable", status, "names must remain isolated to the connection")
}

func TestSenderNameLookupFailureDeadlineAndVerifiedIdentity(t *testing.T) {
	for _, failure := range []string{"timeout", "wrong_identity"} {
		t.Run(failure, func(t *testing.T) {
			var requests atomic.Int32
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
				if message.Request == nil {
					return
				}
				require.Equal(t, "GetFullUser", message.Request.Method)
				ref := &wire.GetUserInfoRequest{}
				require.NoError(t, decode(message.Request.Payload, ref))
				require.Equal(t, int64(9988), ref.Peer.AccessHash)
				requests.Add(1)
				if failure == "wrong_identity" {
					fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: message.Request.Index, Payload: marshal(t, &wire.GetUserInfoResponse{User: &wire.FullUser{Id: 43, Name: "Wrong user"}})}})
				}
			})
			client := fake.client()
			client.senderLookupAfter = time.Time{}
			connectTest(t, client, acceptingSink)
			client.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 9988})
			event := receivedMessage(t, &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}, nil)
			start := time.Now()
			prepared := client.prepareEvent(context.Background(), event)
			require.Less(t, time.Since(start), time.Second)
			require.Equal(t, event.ID, prepared.ID)
			require.Equal(t, "synthetic", prepared.Message.Body)
			require.Empty(t, prepared.Message.SenderDisplayName)
			require.Equal(t, "unavailable", prepared.Message.SenderNameStatus)
			client.prepareEvent(context.Background(), event)
			require.Equal(t, int32(1), requests.Load(), "failed enrichment is negatively cached")
		})
	}
}

func TestSenderNameCacheBoundExpiryAndContactPopulation(t *testing.T) {
	c := New(Options{})
	require.NoError(t, c.rememberConversationRefs([]*wire.User{{Id: 42, Name: "history sender"}}, nil, nil, nil))
	name, status := c.cachedSenderDisplayName("42")
	require.Equal(t, "history sender", name)
	require.Equal(t, "available", status)
	c.senderNames["42"] = senderName{name: "expired", expires: time.Now().Add(-time.Second)}
	name, status = c.cachedSenderDisplayName("42")
	require.Empty(t, name)
	require.Equal(t, "unavailable", status)
	for i := uint32(1); i < maxSenderNames+100; i++ {
		c.rememberUserName(&wire.User{Id: i, Name: "synthetic"})
	}
	require.Len(t, c.senderNames, maxSenderNames)
	require.Empty(t, safeDisplayName("unsafe\nname", ""))
}

func TestColdOwnMessageResolvesAuthenticatedSelfWithoutCachedHash(t *testing.T) {
	var requests atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
		if message.Request == nil {
			return
		}
		require.Equal(t, "GetFullUser", message.Request.Method)
		ref := &wire.GetUserInfoRequest{}
		require.NoError(t, decode(message.Request.Payload, ref))
		require.Equal(t, uint32(12345), ref.Peer.Id)
		require.Zero(t, ref.Peer.AccessHash)
		requests.Add(1)
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: message.Request.Index, Payload: marshal(t, &wire.GetUserInfoResponse{User: &wire.FullUser{Id: 12345, Name: "Account name"}})}})
	})
	client := fake.client()
	client.senderLookupAfter = time.Time{}
	connectTest(t, client, acceptingSink)
	event := receivedMessage(t, &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}, nil)
	event.SenderID, event.Direction = "12345", "outgoing"
	prepared := client.prepareEvent(context.Background(), event)
	require.Equal(t, "Account name", prepared.Message.SenderDisplayName)
	require.Equal(t, "available", prepared.Message.SenderNameStatus)
	require.Equal(t, "12345", prepared.Message.From)
	require.True(t, *prepared.Message.IsFromMe)
	client.prepareEvent(context.Background(), event)
	require.Equal(t, int32(1), requests.Load())
}
