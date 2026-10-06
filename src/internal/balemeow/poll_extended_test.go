package balemeow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func TestPollCreateThenSendUsesJournalRIDAndExPeer(t *testing.T) {
	var step atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var response proto.Message
		switch r.Method {
		case "CreatePoll":
			q := &wire.PollCreateRequest{}
			if step.Add(1) != 1 || r.Service != pollService || decode(r.Payload, q) != nil || q.ExPeer.GetType() != 3 || q.ExPeer.GetAccessHash() != 678 || q.CreateAt != 0 || !q.PollMessage.IsAnonymous || q.PollMessage.Type != 1 || q.PollMessage.Options[0].Id != 0 || q.PollMessage.Options[1].Id != 1 {
				t.Error("incorrect poll creation")
			}
			response = &wire.PollCreateResponse{PollId: -9007199254740993}
		case "SendMessage":
			q := &wire.SendMessageRequest{}
			if step.Add(1) != 2 || r.Service != "bale.messaging.v2.Messaging" || decode(r.Payload, q) != nil || q.Rid != 9007199254740995 || q.Peer.GetType() != 2 || q.ExPeer.GetType() != 3 || q.Message.GetPoll().PollId != -9007199254740993 {
				t.Error("incorrect poll send")
			}
			response = &wire.SendMessageResponse{Date: 1720000000123}
		default:
			t.Errorf("unexpected method %s", r.Method)
			response = &wire.Empty{}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, response)}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	c.rememberRef("channel", &wire.PeerRef{Id: 77, AccessHash: 678})
	result, err := c.pollExtended(context.Background(), "send.poll", json.RawMessage(`{"peer":{"type":"channel","id":"77"},"question":"کدام؟","options":["اول","دوم"],"anonymous":true,"multiple":true,"request_id":"9007199254740995"}`))
	if err != nil || step.Load() != 2 || !strings.Contains(string(result), `"poll_id":"-9007199254740993"`) || strings.Contains(string(result), "access_hash") {
		t.Fatalf("%s %v", result, err)
	}
}

func TestPollSecondStageFailureNeverMakesCreationRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var creates, sends atomic.Int32
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		if r.Method == "CreatePoll" {
			creates.Add(1)
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.PollCreateResponse{PollId: 10})}})
			cancel()
		} else {
			sends.Add(1)
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.Empty{})}})
		}
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	_, err := c.pollExtended(ctx, "send.poll", json.RawMessage(`{"peer":{"type":"user","id":"12345"},"question":"test","options":["one","two"],"request_id":"8"}`))
	var e *domains.Error
	if !errors.As(err, &e) || !e.Ambiguous || e.Retryable || creates.Load() != 1 {
		t.Fatalf("creation must not repeat: %v creates=%d sends=%d", err, creates.Load(), sends.Load())
	}
}

func TestPollOfficialReadVoteAndClose(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var response proto.Message
		result := &wire.PollResult{PollId: -9007199254740993, VotersCount: 2, RecentVoters: []int64{12345}, ChosenOptionIds: []int64{0}, OptionResults: []*wire.PollOptionResult{{OptionId: 0, VotesCount: 2}}}
		switch r.Method {
		case "Vote":
			q := &wire.PollVoteRequest{}
			if decode(r.Payload, q) != nil || q.PollId != result.PollId || q.VoteAt != 0 || len(q.OptionIds) != 1 || q.OptionIds[0] != 0 {
				t.Error("incorrect vote")
			}
			response = &wire.PollVoteResponse{PollResult: result}
		case "GetPollResults":
			q := &wire.PollResultsRequest{}
			if decode(r.Payload, q) != nil || len(q.PollIds) != 1 || q.PollIds[0] != result.PollId {
				t.Error("incorrect results")
			}
			response = &wire.PollResultsResponse{PollResults: []*wire.PollResult{result}}
		case "GetFullPollResult":
			response = &wire.PollFullResultsResponse{FullPollResult: []*wire.PollFullOptionResult{{OptionId: 0, VotesCount: 1, Voters: []*wire.PollVoter{{UserId: 12345, VotedAt: 1720000000123}}}}}
		case "ClosePoll":
			q := &wire.PollIDRequest{}
			if decode(r.Payload, q) != nil || q.PollId != result.PollId {
				t.Error("incorrect close")
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
	for _, tc := range []struct{ op, body string }{
		{"poll.vote", `{"poll_id":"-9007199254740993","option_ids":[0],"request_id":"2"}`},
		{"poll.results", `{"poll_ids":["-9007199254740993"]}`},
		{"poll.full_results", `{"poll_id":"-9007199254740993"}`},
		{"poll.close", `{"poll_id":"-9007199254740993","request_id":"3"}`},
	} {
		result, err := c.pollExtended(context.Background(), tc.op, json.RawMessage(tc.body))
		if err != nil || !strings.Contains(string(result), `"poll_id":"-9007199254740993"`) {
			t.Errorf("%s: %s %v", tc.op, result, err)
		}
	}
}

func TestPollValidationBeforeNetwork(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body, code string }{
		{"send.poll", `{"question":"x","options":["a","b"]}`, "INVALID_REQUEST_ID"},
		{"send.poll", `{"question":"x","options":["a"],"request_id":"1"}`, "INVALID_REQUEST"},
		{"poll.vote", `{"poll_id":"1","option_ids":[1,1],"request_id":"1"}`, "INVALID_REQUEST"},
		{"poll.vote", `{"poll_id":"1","option_ids":[1],"retract":true,"request_id":"1"}`, "INVALID_REQUEST"},
		{"poll.close", `{"poll_id":"0","request_id":"1"}`, "INVALID_REQUEST"},
		{"poll.results", `{"poll_ids":["1","1"]}`, "INVALID_REQUEST"},
		{"poll.results", `{"poll_ids":[],"secret":"invalid"}`, "INVALID_REQUEST"},
	} {
		_, err := c.pollExtended(context.Background(), tc.op, json.RawMessage(tc.body))
		if codeOf(err) != tc.code {
			t.Errorf("%s %v", tc.op, err)
		}
	}
}
