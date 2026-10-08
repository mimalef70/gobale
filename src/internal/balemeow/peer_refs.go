package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

// History and dialogs return OutUserPeer/OutGroupPeer alongside their ordinary
// InPeer values. Only provider-returned references enter this per-account cache.
func (c *Client) rememberConversationRefs(fullUsers []*wire.User, fullGroups []*wire.Group, users, groups []*wire.PeerRef) error {
	if len(fullUsers)+len(fullGroups)+len(users)+len(groups) > 4096 {
		return protocolError()
	}
	allUsers := make([]*wire.PeerRef, 0, len(fullUsers)+len(users))
	allGroups := make([]*wire.PeerRef, 0, len(fullGroups)+len(groups))
	groupKinds := map[uint32]int32{}
	for _, user := range fullUsers {
		if user == nil || user.Id == 0 || len(user.Name) > 4096 || len(user.GetLocalName().GetValue()) > 4096 {
			return protocolError()
		}
		allUsers = append(allUsers, &wire.PeerRef{Id: user.Id, AccessHash: user.AccessHash})
	}
	for _, group := range fullGroups {
		if group == nil || group.Id == 0 || group.GroupType < 0 || group.GroupType > 2 {
			return protocolError()
		}
		groupKinds[group.Id] = group.GroupType
		allGroups = append(allGroups, &wire.PeerRef{Id: group.Id, AccessHash: group.AccessHash})
	}
	users = append(allUsers, users...)
	// Bare refs do not distinguish channels. Only accompanying full entities
	// establish subtype; a ref alone may refresh an already verified subtype.
	for _, ref := range groups {
		if ref == nil || ref.Id == 0 {
			return protocolError()
		}
		if _, known := groupKinds[ref.Id]; known {
			allGroups = append(allGroups, ref)
		}
	}
	groups = allGroups
	if len(users)+len(groups) > 4096 {
		return protocolError()
	}
	for _, refs := range [][]*wire.PeerRef{users, groups} {
		for _, r := range refs {
			if r == nil || r.Id == 0 {
				return protocolError()
			}
		}
	}
	for _, r := range users {
		c.rememberRef("user", r)
	}
	for _, user := range fullUsers {
		c.rememberUserName(user)
	}
	for _, r := range groups {
		if _, err := c.rememberGroupKind(r, groupKinds[r.Id]); err != nil {
			return err
		}
	}
	return nil
}
func (c *Client) cachedUserRef(peer domains.Peer, id uint32) (*wire.PeerRef, bool) {
	c.mu.Lock()
	hash, ok := c.peerHashes[peer.Key()]
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	return &wire.PeerRef{Id: id, AccessHash: hash}, true
}
func (c *Client) resolvedUserRef(ctx context.Context, peer domains.Peer) (*wire.PeerRef, error) {
	value, err := encodePeer(peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != "user" {
		return nil, boundedError("INVALID_PEER", "a user peer is required", 400)
	}
	if ref, ok := c.cachedUserRef(peer, value.Id); ok {
		return ref, nil
	}
	// One deadline covers contacts and every fallback page, not a fresh timeout
	// for each page. An expired lookup never attempts the subsequent mutation.
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	if _, err := c.contacts(ctx, "contacts.list", json.RawMessage(`{}`)); err != nil {
		return nil, err
	}
	if ref, ok := c.cachedUserRef(peer, value.Id); ok {
		return ref, nil
	}
	return c.dialogUserRef(ctx, peer, value.Id)
}

// dialogUserRef shares the reviewed, bounded provider-reference scan with
// incoming sender enrichment. The caller owns the deadline and contact policy.
func (c *Client) dialogUserRef(ctx context.Context, peer domains.Peer, id uint32) (*wire.PeerRef, error) {
	cursor := int64(-1)
	for page := 0; page < 10; page++ {
		if err := ctx.Err(); err != nil {
			return nil, &domains.Error{Code: "PROVIDER_READ_FAILED", Message: "peer reference lookup deadline exceeded; no mutation was attempted", HTTP: 502, Retryable: true}
		}
		data, err := c.readRPC(ctx, "bale.messaging.v2.Messaging", "LoadDialogs", &wire.DialogsRequest{MinDate: cursor, Limit: 100})
		if err != nil {
			return nil, err
		}
		response := &wire.DialogsResponse{}
		if decode(data, response) != nil || len(response.Dialogs) > 100 {
			return nil, protocolError()
		}
		if err := c.rememberConversationRefs(response.Users, response.Groups, response.UserPeers, response.GroupPeers); err != nil {
			return nil, err
		}
		if ref, ok := c.cachedUserRef(peer, id); ok {
			return ref, nil
		}
		if len(response.Dialogs) < 100 {
			break
		}
		next := int64(0)
		for _, d := range response.Dialogs {
			if d == nil {
				return nil, protocolError()
			}
			if d.SortDate > 0 && (next == 0 || d.SortDate < next) {
				next = d.SortDate
			}
		}
		// Do not guess through a non-advancing or absent provider cursor.
		if next == 0 || (cursor != -1 && next >= cursor) {
			break
		}
		cursor = next
	}
	return nil, boundedError("PEER_NOT_FOUND", "user reference was not found in contacts or the bounded recent conversation scan", 404)
}
