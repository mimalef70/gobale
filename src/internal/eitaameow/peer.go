package eitaameow

import (
	"strconv"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

const supergroupPrefix = "channel_"

// Public IDs distinguish classic chats from channel-backed supergroups even
// when their decimal wire IDs coincide. Decoding stays inside this adapter.
func peerWireID(peer domains.Peer) string {
	if peer.Type == "group" && strings.HasPrefix(peer.ID, supergroupPrefix) {
		return strings.TrimPrefix(peer.ID, supergroupPrefix)
	}
	return peer.ID
}
func isSupergroup(peer domains.Peer) bool {
	return peer.Type == "group" && strings.HasPrefix(peer.ID, supergroupPrefix)
}
func entityPeer(entity object) (domains.Peer, error) {
	id := entity.num("id")
	if id <= 0 {
		return domains.Peer{}, protocolError()
	}
	peer := domains.Peer{ID: strconv.FormatInt(id, 10)}
	switch entity.str("_") {
	case "user", "userEmpty":
		peer.Type = "user"
	case "chat", "chatForbidden":
		peer.Type = "group"
	case "channel", "channelForbidden":
		peer.Type = "channel"
		if entity["megagroup"] == true {
			peer.Type = "group"
			peer.ID = supergroupPrefix + peer.ID
		}
	default:
		return domains.Peer{}, protocolError()
	}
	return peer, nil
}
func peerReferenceMatches(peer domains.Peer, ref object) bool {
	id := peerWireID(peer)
	if !(Contract{}).ValidateUserID(id) {
		return false
	}
	switch peer.Type {
	case "user":
		return ref.str("_") == "inputPeerUser" && strconv.FormatInt(ref.num("user_id"), 10) == id
	case "channel":
		return ref.str("_") == "inputPeerChannel" && strconv.FormatInt(ref.num("channel_id"), 10) == id
	case "group":
		if isSupergroup(peer) {
			return ref.str("_") == "inputPeerChannel" && strconv.FormatInt(ref.num("channel_id"), 10) == id
		}
		return ref.str("_") == "inputPeerChat" && strconv.FormatInt(ref.num("chat_id"), 10) == id
	}
	return false
}
