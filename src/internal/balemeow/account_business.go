package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"unicode/utf8"
)

// accountBusinessCall exposes only reviewed, bounded operations. Financial
// tokens and payment instructions are not accepted by this ordinary journal.
func (c *Client) accountBusinessCall(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	if op == "message.upvoters" || op == "message.upvote" || op == "message.upvote.remove" {
		return c.accountMagazineCall(ctx, op, raw)
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	var p struct {
		Peer        domains.Peer      `json:"peer"`
		Kind        int32             `json:"kind"`
		Description string            `json:"description"`
		Source      *int32            `json:"source"`
		Messages    []reactionMessage `json:"messages"`
		StoryIDs    []string          `json:"story_ids"`
		RequestID   string            `json:"request_id"`
	}
	if len(raw) > 128<<10 || decodeAccountBody(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid report body", 400)
	}
	if op != "report.peer" && op != "report.messages" && op != "report.story" && op != "report.dismiss" {
		return nil, domains.Unsupported(op)
	}
	if err := requireAccountMutationID(p.RequestID); err != nil {
		return nil, err
	}
	if !utf8.ValidString(p.Description) || utf8.RuneCountInString(p.Description) > 1024 {
		return nil, boundedError("INVALID_REQUEST", "description exceeds 1024 characters", 400)
	}
	if op != "report.dismiss" && (p.Kind < 1 || p.Kind > 6) {
		return nil, boundedError("INVALID_REQUEST", "report kind must be 1 to 6", 400)
	}
	if p.Source != nil && (*p.Source < 0 || *p.Source > 4) {
		return nil, boundedError("INVALID_REQUEST", "report source must be 0 to 4", 400)
	}
	var mids []*wire.ReactionMessageID
	if op == "report.messages" {
		var err error
		mids, err = reactionMessageIDs(p.Messages)
		if err != nil {
			return nil, err
		}
	}
	var exPeer *wire.Peer
	var storyIDs []*wire.StringValue
	if op == "report.story" {
		if len(p.StoryIDs) < 1 || len(p.StoryIDs) > 100 {
			return nil, boundedError("INVALID_REQUEST", "story_ids requires 1 to 100 distinct opaque story IDs", 400)
		}
		seen := map[string]bool{}
		for _, id := range p.StoryIDs {
			if !validStoryID(id) || seen[id] {
				return nil, boundedError("INVALID_REQUEST", "story_ids requires valid distinct opaque story IDs", 400)
			}
			seen[id] = true
			storyIDs = append(storyIDs, &wire.StringValue{Value: id})
		}
	} else {
		peer, err := c.verifiedAccountPeer(ctx, p.Peer)
		if err != nil {
			return nil, err
		}
		exPeer = extendedPeer(peer, c.canonicalPeer(p.Peer))
	}
	method := "ReportInappropriateContent"
	var request proto.Message
	if op == "report.dismiss" {
		method = "ReportDismiss"
		request = &wire.AccountReportDismissRequest{Peer: exPeer}
	} else {
		report := &wire.AccountReport{Kind: p.Kind, Description: p.Description}
		if op == "report.peer" {
			source := int32(1)
			if p.Source != nil {
				source = *p.Source
			}
			report.PeerReport = &wire.AccountPeerReport{Source: source, Peer: exPeer}
		} else if op == "report.messages" {
			report.MessageReport = &wire.AccountMessageReport{Peer: exPeer, Mids: mids}
		} else {
			report.StoryReport = &wire.AccountStoryReport{StoryId: storyIDs}
		}
		request = &wire.AccountReportRequest{Report: report}
	}
	data, err := c.rpc(ctx, "bale.report.v1.Report", method, request)
	if err != nil {
		return nil, err
	}
	if decode(data, &wire.Empty{}) != nil {
		return nil, ambiguous()
	}
	return json.RawMessage(`{"acknowledged":true}`), nil
}
