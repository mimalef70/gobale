package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"strconv"
	"strings"
)

func (c *Client) contacts(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Query string `json:"query"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid contact request", 400)
	}
	var users []*wire.User
	var refs []*wire.PeerRef
	var groups []*wire.Group
	var groupRefs []*wire.PeerRef
	if op == "contacts.list" {
		data, err := c.readRPC(ctx, "bale.users.v1.Users", "GetContacts", &wire.ContactsRequest{})
		if err != nil {
			return nil, err
		}
		response := &wire.ContactsResponse{}
		if decode(data, response) != nil {
			return nil, protocolError()
		}
		users = response.Users
		refs = response.Peers
	} else {
		p.Query = strings.TrimSpace(p.Query)
		if p.Query == "" || len(p.Query) > 256 {
			return nil, boundedError("INVALID_REQUEST", "query must contain 1 to 256 bytes", 400)
		}
		data, err := c.readRPC(ctx, "bale.users.v1.Users", "SearchContacts", &wire.ContactsSearchRequest{Request: p.Query})
		if err != nil {
			return nil, err
		}
		response := &wire.ContactsSearchResponse{}
		if decode(data, response) != nil {
			return nil, protocolError()
		}
		users = response.Users
		refs = response.UserPeers
		groups = response.Groups
		groupRefs = response.GroupPeers
	}
	if len(users)+len(refs)+len(groups)+len(groupRefs) > 4096 {
		return nil, protocolError()
	}
	out := []map[string]any{}
	seen := map[uint32]bool{}
	for _, u := range users {
		if u.Id == 0 || len(u.Name) > 4096 || len(u.GetNick().GetValue()) > 4096 {
			return nil, protocolError()
		}
		c.rememberRef("user", &wire.PeerRef{Id: u.Id, AccessHash: u.AccessHash})
		seen[u.Id] = true
		out = append(out, map[string]any{"peer": domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(u.Id), 10)}, "name": u.Name, "username": u.GetNick().GetValue(), "is_bot": u.GetIsBot().GetValue(), "is_deleted": u.GetIsDeleted().GetValue()})
	}
	for _, r := range refs {
		if r.Id == 0 {
			return nil, protocolError()
		}
		c.rememberRef("user", r)
		if !seen[r.Id] {
			out = append(out, map[string]any{"peer": domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(r.Id), 10)}})
		}
	}
	groupOut := []map[string]any{}
	for _, g := range groups {
		if g.Id == 0 || len(g.Title) > 4096 {
			return nil, protocolError()
		}
		if g.GroupType == 1 {
			continue
		}
		c.rememberRef("group", &wire.PeerRef{Id: g.Id, AccessHash: g.AccessHash})
		groupOut = append(groupOut, map[string]any{"peer": domains.Peer{Type: "group", ID: strconv.FormatUint(uint64(g.Id), 10)}, "title": g.Title})
	}
	// Group refs alone do not state whether the peer is a channel; avoid silently
	// treating a channel result as a legacy group.
	return json.Marshal(map[string]any{"contacts": out, "groups": groupOut})
}
