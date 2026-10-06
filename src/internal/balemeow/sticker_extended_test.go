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

func TestStickerInventoryAndSendResolvePrivateReferences(t *testing.T) {
	for _, animated := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "legacy_animated"}[animated], func(t *testing.T) {
			var sent atomic.Int32
			img := &wire.StickerImage{File: &wire.FileLocation{FileId: -9007199254740993, AccessHash: 887766}, Width: 512, Height: 512, FileSize: 500}
			pack := &wire.StickerCollection{Id: 7, AccessHash: 123456789, Name: &wire.StringValue{Value: "test"}, Stickers: []*wire.StickerDescriptor{{Id: 12, Emoji: &wire.StringValue{Value: "💙"}, Image512: img}}, AnimatedStickers: []*wire.AnimatedStickerDescriptor{{Id: 12, FileLocation: img}}}
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				r := m.Request
				var response proto.Message
				switch r.Method {
				case "LoadOwnStickers":
					response = &wire.StickerListResponse{OwnStickers: []*wire.StickerCollection{pack}}
				case "LoadStickerCollection":
					q := &wire.StickerCollectionRequest{}
					if decode(r.Payload, q) != nil || q.Id != 7 || q.AccessHash != 123456789 {
						t.Error("lost account-owned reference")
					}
					response = &wire.StickerCollectionResponse{Collection: pack}
				case "SendMessage":
					q := &wire.SendMessageRequest{}
					if decode(r.Payload, q) != nil || q.Rid != 9007199254740995 || q.Peer.GetType() != 2 || q.ExPeer.GetType() != 3 {
						t.Error("incorrect message envelope")
					}
					if animated {
						if q.Message.GetAnimatedSticker().GetCollectionAccessHash().GetValue() != 123456789 || q.Message.GetAnimatedSticker().GetFileLocation().GetFile().GetFileId() != -9007199254740993 {
							t.Error("incorrect animated descriptor")
						}
					} else {
						if q.Message.GetSticker().GetCollectionAccessHash().GetValue() != 123456789 || q.Message.GetSticker().GetImage512().GetFile().GetFileId() != -9007199254740993 {
							t.Error("incorrect sticker descriptor")
						}
					}
					sent.Add(1)
					response = &wire.SendMessageResponse{Date: 1720000000123}
				case "AddStickerPack", "RemoveStickerPack":
					q := &wire.StickerPackRequest{}
					if decode(r.Payload, q) != nil || q.Id != 7 {
						t.Error("incorrect pack")
					}
					response = &wire.Empty{}
				default:
					t.Errorf("unexpected %s", r.Method)
					response = &wire.Empty{}
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			c.rememberRef("channel", &wire.PeerRef{Id: 77, AccessHash: 9})
			for _, tc := range []struct{ op, body string }{{"sticker.list", `{}`}, {"sticker.get", `{"collection_id":"7"}`}, {"sticker.pack.add", `{"collection_id":"7","request_id":"11"}`}, {"sticker.pack.remove", `{"collection_id":"7","request_id":"12"}`}} {
				result, err := c.stickerExtended(context.Background(), tc.op, json.RawMessage(tc.body))
				if err != nil || strings.Contains(string(result), "123456789") || strings.Contains(string(result), "887766") || strings.Contains(string(result), "access_hash") {
					t.Fatalf("%s: %s %v", tc.op, result, err)
				}
			}
			body := `{"peer":{"type":"channel","id":"77"},"collection_id":"7","sticker_id":"12","request_id":"9007199254740995","animated":false}`
			if animated {
				body = strings.Replace(body, `"animated":false`, `"animated":true`, 1)
			}
			result, err := c.stickerExtended(context.Background(), "send.sticker", json.RawMessage(body))
			if err != nil || sent.Load() != 1 || !strings.Contains(string(result), `"message_id":"9007199254740995"`) {
				t.Fatalf("%s %v", result, err)
			}
		})
	}
}

func TestStickerLookupPaginatesAndRejectsLoops(t *testing.T) {
	for _, loop := range []bool{false, true} {
		t.Run(map[bool]string{false: "found_next_page", true: "repeated_cursor"}[loop], func(t *testing.T) {
			var pages atomic.Int32
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				r := m.Request
				var response proto.Message
				if r.Method == "LoadOwnStickers" {
					q := &wire.StickerListRequest{}
					if decode(r.Payload, q) != nil {
						t.Error("bad cursor")
					}
					pages.Add(1)
					if q.GetOffset().GetValue() == "next" && !loop {
						response = &wire.StickerListResponse{OwnStickers: []*wire.StickerCollection{{Id: 7, AccessHash: 99}}}
					} else {
						response = &wire.StickerListResponse{Offset: &wire.StringValue{Value: "next"}}
					}
				} else if r.Method == "LoadStickerCollection" {
					response = &wire.StickerCollectionResponse{Collection: &wire.StickerCollection{Id: 7, AccessHash: 99}}
				} else {
					t.Errorf("unexpected %s", r.Method)
					response = &wire.Empty{}
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			_, err := c.stickerExtended(context.Background(), "sticker.get", json.RawMessage(`{"collection_id":"7"}`))
			if (!loop && err != nil) || (loop && codeOf(err) != "PROTOCOL_ERROR") || pages.Load() != 2 {
				t.Fatalf("loop=%v pages=%d error=%v", loop, pages.Load(), err)
			}
		})
	}
}

func TestStickerValidationAndMissingInventoryNeverSend(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body, code string }{
		{"send.sticker", `{"collection_id":"7","sticker_id":"12"}`, "INVALID_REQUEST_ID"},
		{"send.sticker", `{"collection_id":"7","sticker_id":"0","request_id":"1"}`, "INVALID_REQUEST"},
		{"sticker.get", `{"collection_id":"2147483648"}`, "INVALID_REQUEST"},
		{"sticker.get", `{"collection_id":"7","access_hash":"1"}`, "INVALID_REQUEST"},
	} {
		_, err := c.stickerExtended(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(err) != tc.code {
			t.Errorf("%s %v", tc.op, err)
		}
	}
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		if m.Request.Method != "LoadOwnStickers" {
			t.Errorf("must not look up or send unknown collection: %s", m.Request.Method)
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.StickerListResponse{})}})
	})
	c = fake.client()
	connectTest(t, c, acceptingSink)
	_, err := c.stickerExtended(context.Background(), "send.sticker", json.RawMessage(`{"peer":{"type":"user","id":"12345"},"collection_id":"7","sticker_id":"12","request_id":"1"}`))
	if codeOf(err) != "STICKER_COLLECTION_NOT_FOUND" {
		t.Fatalf("%v", err)
	}
}
