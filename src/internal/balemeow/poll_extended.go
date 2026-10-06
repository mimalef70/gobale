package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

const pollService = "bale.poll.v1.Poll"

type pollPayload struct {
	Peer      domains.Peer `json:"peer"`
	Question  string       `json:"question"`
	Options   []string     `json:"options"`
	Anonymous bool         `json:"anonymous"`
	Multiple  bool         `json:"multiple"`
	PollID    string       `json:"poll_id"`
	PollIDs   []string     `json:"poll_ids"`
	OptionIDs []int32      `json:"option_ids"`
	Retract   bool         `json:"retract"`
	RequestID string       `json:"request_id"`
}

func (c *Client) pollExtended(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	var p pollPayload
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > 64<<10 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return nil, groupRequestError("invalid poll operation body")
	}
	var rid, pollID int64
	var err error
	switch op {
	case "send.poll", "poll.vote", "poll.close":
		rid, err = positiveID(p.RequestID)
		if err != nil {
			return nil, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before poll mutation", 400)
		}
	case "poll.results", "poll.full_results":
	default:
		return nil, domains.Unsupported(op)
	}
	if op == "poll.vote" || op == "poll.close" || op == "poll.full_results" {
		pollID, err = messageID(p.PollID)
		if err != nil {
			return nil, groupRequestError("poll_id must be a nonzero signed decimal int64")
		}
	}
	switch op {
	case "send.poll":
		if !validPollText(p.Question, 300) || len(p.Options) < 2 || len(p.Options) > 10 {
			return nil, groupRequestError("question requires 1..300 characters and options requires 2..10 entries")
		}
		poll := &wire.PollMessage{Question: p.Question, IsAnonymous: p.Anonymous}
		if p.Multiple {
			poll.Type = 1
		}
		for i, option := range p.Options {
			if !validPollText(option, 100) {
				return nil, groupRequestError("each option requires 1..100 characters")
			}
			// Option IDs are explicit on the wire. A stable zero-based sequence makes
			// voting independent of display labels or local array reordering.
			poll.Options = append(poll.Options, &wire.PollOption{Id: int32(i), Text: option})
		}
		peer, err := c.messagePeer(ctx, p.Peer)
		if err != nil {
			return nil, err
		}
		exPeer := extendedPeer(peer, p.Peer)
		data, err := c.rpc(ctx, pollService, "CreatePoll", &wire.PollCreateRequest{PollMessage: poll, ExPeer: exPeer})
		if err != nil {
			return nil, err
		}
		created := &wire.PollCreateResponse{}
		if decode(data, created) != nil || created.PollId == 0 {
			return nil, ambiguous()
		}
		poll.PollId = created.PollId
		data, err = c.rpc(ctx, "bale.messaging.v2.Messaging", "SendMessage", &wire.SendMessageRequest{Peer: peer, ExPeer: exPeer, Rid: rid, Message: &wire.Message{Poll: poll}})
		// CreatePoll has no client request ID. Retrying this compound operation
		// after any second-stage failure could create another poll, including when
		// SendMessage failed before writing. Preserve unknown for reconciliation.
		if err != nil {
			return nil, ambiguous()
		}
		sent := &wire.SendMessageResponse{}
		if decode(data, sent) != nil || sent.Date <= 0 {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "poll_id": strconv.FormatInt(created.PollId, 10), "message_id": p.RequestID, "date": time.UnixMilli(sent.Date).UTC()})
	case "poll.vote":
		if len(p.OptionIDs) > 10 || (!p.Retract && len(p.OptionIDs) == 0) || (p.Retract && len(p.OptionIDs) != 0) {
			return nil, groupRequestError("a vote needs 1..10 option_ids; retraction needs an empty option_ids array")
		}
		seen := map[int32]bool{}
		for _, id := range p.OptionIDs {
			if id < 0 || seen[id] {
				return nil, groupRequestError("option_ids must be distinct nonnegative int32 values")
			}
			seen[id] = true
		}
		data, err := c.rpc(ctx, pollService, "Vote", &wire.PollVoteRequest{PollId: pollID, IsRetract: p.Retract, OptionIds: p.OptionIDs})
		if err != nil {
			return nil, err
		}
		response := &wire.PollVoteResponse{}
		if decode(data, response) != nil || response.PollResult == nil || response.PollResult.PollId != pollID {
			return nil, ambiguous()
		}
		result, err := safePollResult(response.PollResult)
		if err != nil {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "result": result})
	case "poll.close":
		data, err := c.rpc(ctx, pollService, "ClosePoll", &wire.PollIDRequest{PollId: pollID})
		if err != nil {
			return nil, err
		}
		if decode(data, &wire.Empty{}) != nil {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "poll_id": p.PollID})
	case "poll.results":
		if len(p.PollIDs) < 1 || len(p.PollIDs) > 100 {
			return nil, groupRequestError("poll_ids requires 1..100 entries")
		}
		ids := make([]int64, 0, len(p.PollIDs))
		seen := map[int64]bool{}
		for _, id := range p.PollIDs {
			n, e := messageID(id)
			if e != nil || seen[n] {
				return nil, groupRequestError("poll_ids must be distinct nonzero signed int64 values")
			}
			seen[n] = true
			ids = append(ids, n)
		}
		data, err := c.readRPC(ctx, pollService, "GetPollResults", &wire.PollResultsRequest{PollIds: ids})
		if err != nil {
			return nil, err
		}
		response := &wire.PollResultsResponse{}
		if decode(data, response) != nil || len(response.PollResults) > len(ids) {
			return nil, protocolError()
		}
		results := make([]map[string]any, 0, len(response.PollResults))
		returned := map[int64]bool{}
		for _, r := range response.PollResults {
			if r == nil || !seen[r.PollId] || returned[r.PollId] {
				return nil, protocolError()
			}
			returned[r.PollId] = true
			item, e := safePollResult(r)
			if e != nil {
				return nil, e
			}
			results = append(results, item)
		}
		return json.Marshal(map[string]any{"results": results})
	case "poll.full_results":
		data, err := c.readRPC(ctx, pollService, "GetFullPollResult", &wire.PollIDRequest{PollId: pollID})
		if err != nil {
			return nil, err
		}
		response := &wire.PollFullResultsResponse{}
		if decode(data, response) != nil || len(response.FullPollResult) > 100 {
			return nil, protocolError()
		}
		results := make([]map[string]any, 0, len(response.FullPollResult))
		total := 0
		for _, r := range response.FullPollResult {
			if r == nil || r.OptionId < 0 || r.VotesCount < 0 || len(r.Voters) > 10000 {
				return nil, protocolError()
			}
			total += len(r.Voters)
			if total > 10000 {
				return nil, protocolError()
			}
			voters := make([]map[string]any, 0, len(r.Voters))
			for _, v := range r.Voters {
				if v == nil || v.UserId == 0 || v.VotedAt < 0 {
					return nil, protocolError()
				}
				voters = append(voters, map[string]any{"user_id": strconv.FormatUint(uint64(v.UserId), 10), "voted_at": strconv.FormatInt(v.VotedAt, 10)})
			}
			results = append(results, map[string]any{"option_id": r.OptionId, "votes_count": r.VotesCount, "voters": voters})
		}
		return json.Marshal(map[string]any{"poll_id": p.PollID, "results": results})
	}
	return nil, domains.Unsupported(op)
}

func validPollText(value string, max int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= max
}

func safePollResult(r *wire.PollResult) (map[string]any, error) {
	if r == nil || r.PollId == 0 || r.VotersCount < 0 || len(r.OptionResults) > 100 || len(r.RecentVoters) > 1000 || len(r.ChosenOptionIds) > 100 {
		return nil, protocolError()
	}
	options := make([]map[string]any, 0, len(r.OptionResults))
	seen := map[int32]bool{}
	for _, o := range r.OptionResults {
		if o == nil || o.OptionId < 0 || o.VotesCount < 0 || seen[o.OptionId] {
			return nil, protocolError()
		}
		seen[o.OptionId] = true
		options = append(options, map[string]any{"option_id": o.OptionId, "votes_count": o.VotesCount})
	}
	recent := make([]string, 0, len(r.RecentVoters))
	for _, id := range r.RecentVoters {
		if id <= 0 || id > 4294967295 {
			return nil, protocolError()
		}
		recent = append(recent, strconv.FormatInt(id, 10))
	}
	chosen := make([]int32, 0, len(r.ChosenOptionIds))
	for _, id := range r.ChosenOptionIds {
		if id < 0 || id > 2147483647 {
			return nil, protocolError()
		}
		chosen = append(chosen, int32(id))
	}
	return map[string]any{"poll_id": strconv.FormatInt(r.PollId, 10), "closed": r.IsClosed, "voters_count": r.VotersCount, "options": options, "recent_voters": recent, "chosen_option_ids": chosen}, nil
}
