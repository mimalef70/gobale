package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func TestStoryImageUploadsBeforePublication(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	var uploads, published atomic.Int32
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, body) || r.Method != http.MethodPut || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("invalid upload or credential leak")
		}
		uploads.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer tls.Close()
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		switch m.Request.Method {
		case "GetNasimFileUploadUrl":
			q := &wire.FileUploadRequest{}
			if decode(m.Request.Payload, q) != nil || q.GetSendType().GetType() != 1 || q.ExPeer != nil || q.Uid != 12345 {
				t.Error("incorrect story upload shape")
			}
			respond(t, fake, ws, m.Request, &wire.FileUploadResponse{FileId: -987, Url: tls.URL + "/upload"})
		case "AddStory":
			q := &wire.StoryAddRequest{}
			if decode(m.Request.Payload, q) != nil || q.Media.GetFileLocation().GetFileId() != -987 || q.Media.GetFileLocation().GetAccessHash() != 12345 || q.Media.FileSize != int64(len(body)) || q.Text != nil || uploads.Load() != 1 {
				t.Error("invalid publication order or descriptor")
			}
			published.Add(1)
			respond(t, fake, ws, m.Request, &wire.StoryAddResponse{StoryId: "opaque:photo"})
		default:
			t.Errorf("unexpected %s", m.Request.Method)
		}
	})
	c := mediaClient(t, fake, tls, body, MediaInfo{Name: "story.png", ContentType: "image/png", Size: int64(len(body))})
	result, err := c.storyExtended(context.Background(), "story.add", json.RawMessage(`{"media_id":"scoped-media","request_id":"99"}`))
	if err != nil || published.Load() != 1 || strings.Contains(string(result), "access_hash") {
		t.Fatalf("%s %v", result, err)
	}
	_, err = c.storyExtended(context.Background(), "story.add", json.RawMessage(`{"text":"ambiguous choice","media_id":"scoped-media","request_id":"100"}`))
	if codeOf(err) != "INVALID_REQUEST" || uploads.Load() != 1 {
		t.Fatal("both text and image must fail before upload")
	}
}

func TestStoryOfficialRequestsOpaqueIDsAndPrivateMetadata(t *testing.T) {
	var changes atomic.Int32
	story := &wire.StoryRecord{Id: "opaque:story-الف", OwnerUserId: 12345, CreatedAt: 1720000000123, Content: &wire.StoryContent{Text: &wire.StoryText{Text: "سلام"}, Media: &wire.StoryMedia{FileLocation: &wire.FileLocation{FileId: 42, AccessHash: 987654321}, FileSize: 50}}, ContentType: 2, Reactions: []*wire.StoryReaction{{Type: 2, Text: &wire.StringValue{Value: "💙"}}}}
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		if r.Service != storyService {
			t.Error("wrong service")
		}
		var response proto.Message
		switch r.Method {
		case "AddStory":
			q := &wire.StoryAddRequest{}
			if decode(r.Payload, q) != nil || q.Text.GetText() != "سلام" || q.ExpirationType != 1 || q.ExceptionType != 3 || len(q.TagIds) != 1 || q.TagIds[0] != 7 || q.Media != nil {
				t.Error("incorrect add")
			}
			changes.Add(1)
			response = &wire.StoryAddResponse{StoryId: story.Id}
		case "RemoveStory":
			q := &wire.StoryIDRequest{}
			if decode(r.Payload, q) != nil || q.StoryId != story.Id {
				t.Error("opaque ID changed")
			}
			changes.Add(1)
			response = &wire.Empty{}
		case "ReactToStory":
			q := &wire.StoryReactRequest{}
			if decode(r.Payload, q) != nil || q.StoryId != story.Id || q.Reaction != "" || q.Type != 1 || q.ReactionType != 2 || q.GetReactionText().GetValue() != "💙" {
				t.Error("incorrect reaction")
			}
			changes.Add(1)
			response = &wire.Empty{}
		case "GetStories":
			q := &wire.StoryListRequest{}
			if decode(r.Payload, q) != nil || q.GetUnmutual == nil || q.GetUnmutual.Value {
				t.Error("explicit false lost")
			}
			response = &wire.StoryListResponse{Result: []*wire.StoryRecord{story}}
		case "GetStoryById":
			response = &wire.StorySingleResponse{Story: story}
		case "GetViewers":
			q := &wire.StoryViewersRequest{}
			if decode(r.Payload, q) != nil || q.GetPagination().GetPage() != 2 || q.GetPagination().GetLimit() != 20 {
				t.Error("wrong pagination")
			}
			response = &wire.StoryViewersResponse{ViewCount: 1, Viewers: []*wire.StoryViewer{{UserId: 12345, ReactedAt: 1720000000123, ReactionData: []*wire.StoryReaction{{Type: 1}}}}}
		default:
			t.Errorf("unexpected method %s", r.Method)
			response = &wire.Empty{}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	for _, tc := range []struct{ op, body string }{
		{"story.add", `{"text":"سلام","expiration_days":2,"privacy":"contacts","tag_ids":[7],"request_id":"1"}`},
		{"story.delete", `{"story_id":"opaque:story-الف","request_id":"2"}`},
		{"story.react", `{"story_id":"opaque:story-الف","story_type":"channel","reaction_type":"emoji","reaction_text":"💙","request_id":"3"}`},
		{"story.list", `{"get_unmutual":false}`},
		{"story.get", `{"story_id":"opaque:story-الف"}`},
		{"story.viewers", `{"story_id":"opaque:story-الف","page":2,"limit":20}`},
	} {
		result, err := c.storyExtended(context.Background(), tc.op, json.RawMessage(tc.body))
		if err != nil || strings.Contains(string(result), "987654321") || strings.Contains(string(result), "file_location") || strings.Contains(string(result), "access_hash") {
			t.Fatalf("%s: %s %v", tc.op, result, err)
		}
	}
	if changes.Load() != 3 {
		t.Fatal("missing mutation")
	}
}

func TestStoryOtherVariantsAreNotEmptySuccess(t *testing.T) {
	for _, variant := range []string{"channel", "bot", "absent", "mismatch"} {
		t.Run(variant, func(t *testing.T) {
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				response := &wire.StorySingleResponse{}
				switch variant {
				case "channel":
					response.ChannelStory = &wire.Empty{}
				case "bot":
					response.BotStory = &wire.Empty{}
				case "mismatch":
					response.Story = &wire.StoryRecord{Id: "another", OwnerUserId: 12345}
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, response)}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			_, err := c.storyExtended(context.Background(), "story.get", json.RawMessage(`{"story_id":"opaque"}`))
			expected := "FEATURE_NOT_SUPPORTED"
			if variant == "absent" {
				expected = "STORY_NOT_FOUND"
			}
			if variant == "mismatch" {
				expected = "PROTOCOL_ERROR"
			}
			if codeOf(err) != expected {
				t.Fatalf("%s: %v", variant, err)
			}
		})
	}
}

func TestStoryValidationBeforeNetwork(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body, code string }{
		{"story.add", `{"text":"hello"}`, "INVALID_REQUEST_ID"},
		{"story.add", `{"text":"hello","expiration_days":3,"request_id":"1"}`, "INVALID_REQUEST"},
		{"story.add", `{"text":"hello","privacy":"bogus","request_id":"1"}`, "INVALID_REQUEST"},
		{"story.add", `{"text":"hello","tag_ids":[1,1],"request_id":"1"}`, "INVALID_REQUEST"},
		{"story.react", `{"story_id":"a","reaction_type":"emoji","request_id":"1"}`, "INVALID_REQUEST"},
		{"story.react", `{"story_id":"a","reaction_type":"view","reaction_text":"hidden","request_id":"1"}`, "INVALID_REQUEST"},
		{"story.get", `{"story_id":"\u0000"}`, "INVALID_REQUEST"},
		{"story.viewers", `{"story_id":"a","page":-1}`, "INVALID_REQUEST"},
		{"story.list", `{"user_id":"12345"}`, "INVALID_REQUEST"},
	} {
		_, err := c.storyExtended(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(err) != tc.code {
			t.Errorf("%s %v", tc.op, err)
		}
	}
}
