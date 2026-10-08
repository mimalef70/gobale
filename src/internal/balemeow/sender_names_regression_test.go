package balemeow

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func sendSenderNameUpdate(t *testing.T, server *fakeWS, sender uint32, rid int64) {
	t.Helper()
	update := marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{
		Peer: &wire.Peer{Type: 1, Id: sender}, SenderId: sender, Rid: rid,
		Date: 1720000000000 + rid, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}},
	}})
	connections := 0
	server.connections.Range(func(connection, _ any) bool {
		connections++
		server.send(connection.(*websocket.Conn), &wire.ServerMessage{Update: &wire.UpdateEnvelope{Payload: marshal(t, &wire.StreamUpdate{Update: update})}})
		return true
	})
	require.Equal(t, 1, connections)
}

func awaitNamedEvent(t *testing.T, accepted <-chan domains.Event) domains.Event {
	t.Helper()
	select {
	case event := <-accepted:
		require.NotNil(t, event.Message)
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("incoming event did not reach its durable sink within the bounded enrichment window")
		return domains.Event{}
	}
}

func TestFirstNonContactSenderNameUsesVerifiedDialogs(t *testing.T) {
	for _, source := range []string{"full_user", "reference_then_profile"} {
		t.Run(source, func(t *testing.T) {
			var contacts, dialogs, profiles atomic.Int32
			var server *fakeWS
			server = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
				if message.Request == nil {
					return
				}
				request := message.Request
				var response proto.Message
				switch request.Method {
				case "GetContacts":
					contacts.Add(1)
					response = &wire.ContactsResponse{}
				case "LoadDialogs":
					dialogs.Add(1)
					query := &wire.DialogsRequest{}
					require.NoError(t, decode(request.Payload, query))
					require.Equal(t, int64(-1), query.MinDate)
					require.Equal(t, int32(100), query.Limit)
					if source == "full_user" {
						response = &wire.DialogsResponse{Users: []*wire.User{{Id: 42, AccessHash: 987654321, Name: "Public name", LocalName: &wire.StringValue{Value: "نام گفتگوی تازه"}}}}
					} else {
						response = &wire.DialogsResponse{UserPeers: []*wire.PeerRef{{Id: 42, AccessHash: 987654321}}}
					}
				case "GetFullUser":
					profiles.Add(1)
					query := &wire.GetUserInfoRequest{}
					require.NoError(t, decode(request.Payload, query))
					require.Equal(t, uint32(42), query.Peer.Id)
					require.Equal(t, int64(987654321), query.Peer.AccessHash, "only the provider's selected-account dialog reference authorizes this lookup")
					response = &wire.GetUserInfoResponse{User: &wire.FullUser{Id: 42, Name: "نام گفتگوی تازه"}}
				default:
					t.Errorf("unexpected name lookup RPC %q", request.Method)
					return
				}
				respond(t, server, ws, request, response)
			})
			client := server.client()
			client.senderLookupAfter, client.senderContactsAfter = time.Time{}, time.Time{}
			client.opts.RequestTimeout = 2 * time.Second
			accepted := make(chan domains.Event, 2)
			connectTest(t, client, func(_ context.Context, event domains.Event) error { accepted <- event; return nil })
			sendSenderNameUpdate(t, server, 42, 1)
			event := awaitNamedEvent(t, accepted)
			require.Equal(t, "نام گفتگوی تازه", event.Message.SenderDisplayName)
			require.Equal(t, "available", event.Message.SenderNameStatus)
			require.Equal(t, "1", event.Message.ID)
			require.Equal(t, "synthetic", event.Message.Body)
			encoded, err := json.Marshal(event)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "987654321")
			require.NotContains(t, string(encoded), "access_hash")
			require.Equal(t, int32(1), contacts.Load())
			require.Equal(t, int32(1), dialogs.Load())
			wantProfiles := int32(0)
			if source == "reference_then_profile" {
				wantProfiles = 1
			}
			require.Equal(t, wantProfiles, profiles.Load())

			// A later cache refresh cannot mutate the event already accepted by storage.
			client.rememberUserName(&wire.User{Id: 42, Name: "Later name"})
			replayed, err := json.Marshal(event)
			require.NoError(t, err)
			require.Equal(t, encoded, replayed)
			sendSenderNameUpdate(t, server, 42, 2)
			require.Equal(t, "Later name", awaitNamedEvent(t, accepted).Message.SenderDisplayName)
			require.Equal(t, int32(1), dialogs.Load(), "the next warm event needs no additional conversation scan")
		})
	}
}

func TestAdjacentIncomingSendersWaitForRateLimitBeforeDurableAcceptance(t *testing.T) {
	type observation struct {
		id uint32
		at time.Time
	}
	lookups := make(chan observation, 2)
	var server *fakeWS
	server = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
		if message.Request == nil {
			return
		}
		request := message.Request
		switch request.Method {
		case "GetFullUser":
			query := &wire.GetUserInfoRequest{}
			require.NoError(t, decode(request.Payload, query))
			require.Equal(t, int64(query.Peer.Id)+9000, query.Peer.AccessHash)
			lookups <- observation{id: query.Peer.Id, at: time.Now()}
			respond(t, server, ws, request, &wire.GetUserInfoResponse{User: &wire.FullUser{Id: query.Peer.Id, Name: fmt.Sprintf("Sender %d", query.Peer.Id)}})
		case "GetContacts":
			respond(t, server, ws, request, &wire.ContactsResponse{})
		default:
			t.Errorf("unexpected name lookup RPC %q", request.Method)
		}
	})
	client := server.client()
	client.senderLookupAfter = time.Time{}
	client.opts.RequestTimeout = 2 * time.Second
	accepted := make(chan domains.Event, 2)
	connectTest(t, client, func(_ context.Context, event domains.Event) error { accepted <- event; return nil })
	for _, sender := range []uint32{42, 43} {
		client.rememberRef("user", &wire.PeerRef{Id: sender, AccessHash: int64(sender) + 9000})
		sendSenderNameUpdate(t, server, sender, int64(sender))
	}
	first := awaitNamedEvent(t, accepted)
	require.Equal(t, "42", first.Message.ID)
	require.Equal(t, "Sender 42", first.Message.SenderDisplayName)
	require.Equal(t, "available", first.Message.SenderNameStatus)
	firstLookup := <-lookups

	// The update consumer may wait, but the WebSocket reader must continue to
	// route independent RPC responses during that wait.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := client.readRPC(ctx, "bale.users.v1.Users", "GetContacts", &wire.ContactsRequest{})
	require.NoError(t, err, "name enrichment blocked WebSocket response routing")
	second := awaitNamedEvent(t, accepted)
	require.Equal(t, "43", second.Message.ID)
	require.Equal(t, "Sender 43", second.Message.SenderDisplayName, "rate-limited enrichment must run without another incoming message as a trigger")
	require.Equal(t, "available", second.Message.SenderNameStatus)
	secondLookup := <-lookups
	require.Equal(t, uint32(42), firstLookup.id)
	require.Equal(t, uint32(43), secondLookup.id)
	// Allow a small transport scheduling tolerance while enforcing one-second
	// spacing of lookup starts, rather than disabling the existing rate limit.
	require.GreaterOrEqual(t, secondLookup.at.Sub(firstLookup.at), 950*time.Millisecond)
}

func TestSenderNameRateWaitCancellationDoesNotPoisonCache(t *testing.T) {
	var profiles atomic.Int32
	var server *fakeWS
	server = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
		if message.Request != nil {
			require.Equal(t, "GetFullUser", message.Request.Method)
			profiles.Add(1)
			respond(t, server, ws, message.Request, &wire.GetUserInfoResponse{User: &wire.FullUser{Id: 42, Name: "Recovered lookup"}})
		}
	})
	client := server.client()
	client.senderLookupAfter = time.Now().Add(time.Second)
	connectTest(t, client, acceptingSink)
	client.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 11})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	name, status := client.senderDisplayName(ctx, "42")
	require.Empty(t, name)
	require.Equal(t, "unavailable", status)
	require.Less(t, time.Since(started), 300*time.Millisecond)
	require.Equal(t, int32(0), profiles.Load(), "cancelled rate waits must not contact the provider")
	name, status = client.senderDisplayName(context.Background(), "42")
	require.Equal(t, "Recovered lookup", name, "a cancelled wait must not negatively cache a name that was never looked up")
	require.Equal(t, "available", status)
	require.Equal(t, int32(1), profiles.Load())
}

func TestSenderNameRateWaitStopsOnDisconnect(t *testing.T) {
	var requests atomic.Int32
	server := newFakeWS(t, func(_ *websocket.Conn, message *wire.ClientMessage) {
		if message.Request != nil {
			requests.Add(1)
		}
	})
	client := server.client()
	client.senderLookupAfter = time.Now().Add(time.Second)
	connectTest(t, client, acceptingSink)
	client.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 11})
	finished := make(chan string, 1)
	go func() {
		name, _ := client.senderDisplayName(context.Background(), "42")
		finished <- name
	}()
	select {
	case <-finished:
		t.Fatal("connected sender lookup was dropped instead of waiting for its rate-limit slot")
	case <-time.After(30 * time.Millisecond):
	}
	started := time.Now()
	require.NoError(t, client.Disconnect(context.Background()))
	select {
	case name := <-finished:
		require.Empty(t, name)
	case <-time.After(300 * time.Millisecond):
		t.Fatal("disconnected client retained a blocked sender lookup")
	}
	require.Less(t, time.Since(started), 300*time.Millisecond)
	require.Equal(t, int32(0), requests.Load())
}

func TestUnknownSenderDialogLookupIsFinite(t *testing.T) {
	for _, mode := range []string{"missing", "page_cap", "stuck_cursor", "empty_profile", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			var dialogs, profiles atomic.Int32
			var server *fakeWS
			server = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
				if message.Request == nil {
					return
				}
				request := message.Request
				var response proto.Message
				switch request.Method {
				case "GetContacts":
					response = &wire.ContactsResponse{}
				case "LoadDialogs":
					page := dialogs.Add(1)
					if mode == "deadline" {
						return // The complete scan must stop even when its provider does not reply.
					}
					response = &wire.DialogsResponse{}
					if mode == "page_cap" {
						response = &wire.DialogsResponse{Dialogs: dialogPage(100, 100000-int64(page))}
					} else if mode == "stuck_cursor" {
						response = &wire.DialogsResponse{Dialogs: dialogPage(100, 100000)}
					} else if mode == "empty_profile" {
						response = &wire.DialogsResponse{UserPeers: []*wire.PeerRef{{Id: 424242, AccessHash: 22}}}
					}
				case "GetFullUser":
					profiles.Add(1)
					response = &wire.GetUserInfoResponse{User: &wire.FullUser{Id: 424242}}
				default:
					t.Errorf("unexpected name lookup RPC %q", request.Method)
					return
				}
				respond(t, server, ws, request, response)
			})
			client := server.client()
			client.senderLookupAfter, client.senderContactsAfter = time.Time{}, time.Time{}
			client.opts.RequestTimeout = 2 * time.Second
			connectTest(t, client, acceptingSink)
			started := time.Now()
			name, status := client.senderDisplayName(context.Background(), "424242")
			require.Less(t, time.Since(started), time.Second)
			require.Empty(t, name)
			require.Equal(t, "unavailable", status)
			require.Greater(t, dialogs.Load(), int32(0), "a non-contact sender must reach the verified conversation scan")
			require.LessOrEqual(t, dialogs.Load(), int32(10))
			if mode == "stuck_cursor" {
				require.Equal(t, int32(2), dialogs.Load())
			}
			if mode == "page_cap" {
				require.Equal(t, int32(10), dialogs.Load())
			}
			if mode == "empty_profile" {
				require.Equal(t, int32(1), profiles.Load())
			} else {
				require.Equal(t, int32(0), profiles.Load(), "unknown users must never be looked up with guessed access hashes")
			}
			calls := dialogs.Load() + profiles.Load()
			client.senderDisplayName(context.Background(), "424242")
			require.Equal(t, calls, dialogs.Load()+profiles.Load(), "a completed unavailable lookup is negatively cached")
		})
	}
}

func TestSenderNameAndThrottleRemainIsolatedByAccount(t *testing.T) {
	for account := 0; account < 2; account++ {
		wantName := fmt.Sprintf("Account %d contact", account)
		wantHash := int64(9000 + account)
		var server *fakeWS
		server = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
			if message.Request == nil {
				return
			}
			require.Equal(t, "GetFullUser", message.Request.Method)
			query := &wire.GetUserInfoRequest{}
			require.NoError(t, decode(message.Request.Payload, query))
			require.Equal(t, wantHash, query.Peer.AccessHash)
			respond(t, server, ws, message.Request, &wire.GetUserInfoResponse{User: &wire.FullUser{Id: 42, Name: wantName}})
		})
		client := server.client()
		client.senderLookupAfter = time.Time{}
		session := fakeSession()
		session.UserID = fmt.Sprintf("%d", 12345+account)
		require.NoError(t, client.Connect(context.Background(), session, acceptingSink))
		t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
		client.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: wantHash})
		started := time.Now()
		name, status := client.senderDisplayName(context.Background(), "42")
		require.Equal(t, wantName, name)
		require.Equal(t, "available", status)
		require.Less(t, time.Since(started), 800*time.Millisecond, "one account must not inherit another account's rate limit")
	}
}

func TestNextNonContactSenderStillUsesDialogsDuringContactsCooldown(t *testing.T) {
	var contacts, dialogs atomic.Int32
	var server *fakeWS
	server = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
		if message.Request == nil {
			return
		}
		var response proto.Message
		switch message.Request.Method {
		case "GetContacts":
			contacts.Add(1)
			response = &wire.ContactsResponse{}
		case "LoadDialogs":
			// The provider discovers the newly arrived sender on each read. A
			// previous complete contacts response had neither sender.
			id := uint32(41 + dialogs.Add(1))
			response = &wire.DialogsResponse{Users: []*wire.User{{Id: id, AccessHash: int64(id) + 8000, Name: fmt.Sprintf("Dialog %d", id)}}}
		default:
			t.Errorf("unexpected name lookup RPC %q", message.Request.Method)
			return
		}
		respond(t, server, ws, message.Request, response)
	})
	client := server.client()
	client.senderLookupAfter, client.senderContactsAfter = time.Time{}, time.Time{}
	client.opts.RequestTimeout = 2 * time.Second
	accepted := make(chan domains.Event, 2)
	connectTest(t, client, func(_ context.Context, event domains.Event) error { accepted <- event; return nil })
	for _, sender := range []uint32{42, 43} {
		sendSenderNameUpdate(t, server, sender, int64(sender))
		event := awaitNamedEvent(t, accepted)
		require.Equal(t, fmt.Sprintf("Dialog %d", sender), event.Message.SenderDisplayName)
		require.Equal(t, "available", event.Message.SenderNameStatus)
	}
	require.Equal(t, int32(1), contacts.Load(), "preserve the contacts-refresh limit")
	require.Equal(t, int32(2), dialogs.Load(), "a recent contacts refresh must not suppress finding the next new conversation")
}

func TestDialogEnrichmentPreservesUnexpiredNamesFromPartialUsers(t *testing.T) {
	for _, missingName := range []string{"", "unsafe\nname"} {
		t.Run(fmt.Sprintf("name_%q", missingName), func(t *testing.T) {
			var requests atomic.Int32
			var server *fakeWS
			server = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
				if message.Request == nil {
					return
				}
				requests.Add(1)
				require.Equal(t, "LoadDialogs", message.Request.Method)
				respond(t, server, ws, message.Request, &wire.DialogsResponse{Users: []*wire.User{
					{Id: 42, AccessHash: 9042, Name: missingName},
					{Id: 43, AccessHash: 9043, Name: "New conversation sender"},
				}})
			})
			client := server.client()
			client.senderLookupAfter = time.Time{}
			client.opts.RequestTimeout = 2 * time.Second
			accepted := make(chan domains.Event, 2)
			connectTest(t, client, func(_ context.Context, event domains.Event) error { accepted <- event; return nil })
			client.rememberUserName(&wire.User{Id: 42, Name: "Previously verified name"})
			client.mu.Lock()
			originalExpiry := client.senderNames["42"].expires
			client.mu.Unlock()

			sendSenderNameUpdate(t, server, 43, 1)
			first := awaitNamedEvent(t, accepted)
			require.Equal(t, "New conversation sender", first.Message.SenderDisplayName)
			require.Equal(t, "available", first.Message.SenderNameStatus)
			sendSenderNameUpdate(t, server, 42, 2)
			second := awaitNamedEvent(t, accepted)
			require.Equal(t, "Previously verified name", second.Message.SenderDisplayName, "a partial user returned for another sender's lookup must not erase a valid name")
			require.Equal(t, "available", second.Message.SenderNameStatus)
			require.Equal(t, int32(1), requests.Load(), "retaining the known name must not require an extra profile request")
			client.mu.Lock()
			retainedExpiry := client.senderNames["42"].expires
			expired := senderName{name: "Previously verified name", expires: time.Now().Add(-time.Second)}
			client.senderNames["42"] = expired
			client.mu.Unlock()
			require.Equal(t, originalExpiry, retainedExpiry, "partial metadata must not keep stale names alive by extending their TTL")

			client.rememberUserName(&wire.User{Id: 42, Name: missingName})
			name, status := client.cachedSenderDisplayName("42")
			require.Empty(t, name, "expired positive names must not become permanent")
			require.Equal(t, "unavailable", status)
			client.mu.Lock()
			afterPartial := client.senderNames["42"]
			// Only the result of a completed target lookup may become a negative entry.
			client.cacheSenderNameLocked("42", "")
			cached := client.senderNames["42"]
			client.mu.Unlock()
			require.Equal(t, expired, afterPartial, "partial observations must neither revive an expired name nor delay its next lookup")
			require.Empty(t, cached.name)
			require.True(t, cached.expires.After(time.Now()), "a completed unavailable result may still have a bounded negative cache")
		})
	}
}

func TestPartialDialogUserDoesNotSuppressLaterSenderProfileLookup(t *testing.T) {
	for _, partialName := range []string{"", "unsafe\nname"} {
		t.Run(fmt.Sprintf("name_%q", partialName), func(t *testing.T) {
			var dialogs, profiles atomic.Int32
			var server *fakeWS
			server = newFakeWS(t, func(ws *websocket.Conn, message *wire.ClientMessage) {
				if message.Request == nil {
					return
				}
				request := message.Request
				var response proto.Message
				switch request.Method {
				case "LoadDialogs":
					dialogs.Add(1)
					response = &wire.DialogsResponse{Users: []*wire.User{
						{Id: 42, AccessHash: 9042, Name: partialName},
						{Id: 43, AccessHash: 9043, Name: "Dialog sender"},
					}}
				case "GetFullUser":
					profiles.Add(1)
					query := &wire.GetUserInfoRequest{}
					require.NoError(t, decode(request.Payload, query))
					require.Equal(t, uint32(42), query.Peer.Id)
					require.Equal(t, int64(9042), query.Peer.AccessHash)
					response = &wire.GetUserInfoResponse{User: &wire.FullUser{Id: 42, Name: "Profile sender"}}
				default:
					t.Errorf("unexpected name lookup RPC %q", request.Method)
					return
				}
				respond(t, server, ws, request, response)
			})
			client := server.client()
			client.senderLookupAfter = time.Time{}
			client.opts.RequestTimeout = 2 * time.Second
			accepted := make(chan domains.Event, 2)
			connectTest(t, client, func(_ context.Context, event domains.Event) error { accepted <- event; return nil })
			sendSenderNameUpdate(t, server, 43, 1)
			first := awaitNamedEvent(t, accepted)
			require.Equal(t, "Dialog sender", first.Message.SenderDisplayName)
			require.Equal(t, "available", first.Message.SenderNameStatus)
			sendSenderNameUpdate(t, server, 42, 2)
			second := awaitNamedEvent(t, accepted)
			require.Equal(t, "Profile sender", second.Message.SenderDisplayName, "an incidental partial user must not count as a failed lookup of that sender")
			require.Equal(t, "available", second.Message.SenderNameStatus)
			require.Equal(t, int32(1), dialogs.Load())
			require.Equal(t, int32(1), profiles.Load())
		})
	}
}
