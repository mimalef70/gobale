package balemeow

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

// These are explicit reviewed operations, never an arbitrary provider RPC proxy.
func (c *Client) messagingExtended(ctx context.Context, operation string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Peer         domains.Peer   `json:"peer"`
		Peers        []domains.Peer `json:"peers"`
		AddedPeers   []domains.Peer `json:"added_peers"`
		DeletedPeers []domains.Peer `json:"deleted_peers"`
		MessageID    string         `json:"message_id"`
		Date         string         `json:"date"`
		JustMine     bool           `json:"just_mine"`
		Name         string         `json:"name"`
		FolderID     string         `json:"folder_id"`
		IncludeMuted bool           `json:"include_muted_unread_peers"`
		RequestID    string         `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid messaging body", 400)
	}
	contract, ok := domains.OperationDefinition(operation)
	if !ok {
		return nil, domains.Unsupported(operation)
	}
	if contract.Mode == "mutation" {
		if _, err := positiveID(p.RequestID); err != nil {
			return nil, boundedError("INVALID_REQUEST_ID", "persist a request ID before mutation", 400)
		}
	}
	const service = "bale.messaging.v2.Messaging"
	var method string
	var request proto.Message
	var response proto.Message = &wire.Empty{}
	var peer *wire.Peer
	var err error
	if p.Peer.ID != "" {
		peer, err = c.messagePeer(ctx, p.Peer)
		if err != nil {
			return nil, err
		}
		if operation == "message.pin" || operation == "message.unpin" || operation == "message.unpin_all" || operation == "message.pins" {
			peer = extendedPeer(peer, p.Peer)
		}
	}
	position := func() (*wire.MessagePosition, error) {
		rid, e := messageID(p.MessageID)
		if e != nil {
			return nil, boundedError("INVALID_REQUEST", "message_id must be a nonzero signed int64 string", 400)
		}
		date, e := positiveID(p.Date)
		if e != nil {
			return nil, boundedError("INVALID_REQUEST", "original date is required", 400)
		}
		return &wire.MessagePosition{Rid: rid, Date: date}, nil
	}
	peerList := func(list []domains.Peer) ([]*wire.Peer, error) {
		out := make([]*wire.Peer, 0, len(list))
		seen := map[string]bool{}
		for _, p := range list {
			if seen[p.Key()] {
				return nil, boundedError("INVALID_REQUEST", "peers must be unique", 400)
			}
			seen[p.Key()] = true
			r, e := c.messagePeer(ctx, p)
			if e != nil {
				return nil, e
			}
			out = append(out, extendedPeer(r, p))
		}
		return out, nil
	}
	switch operation {
	case "chat.clear", "chat.delete":
		method = "ClearChat"
		if operation == "chat.delete" {
			method = "DeleteChat"
		}
		request = &wire.ChatPeerRequest{Peer: peer}
	case "message.received":
		date, e := positiveID(p.Date)
		if e != nil {
			return nil, boundedError("INVALID_REQUEST", "date is required", 400)
		}
		method = "MessageReceived"
		request = &wire.MessageReadRequest{Peer: peer, Date: date}
	case "message.pin", "message.unpin":
		pos, e := position()
		if e != nil {
			return nil, e
		}
		if operation == "message.pin" {
			method = "PinMessage"
			request = &wire.ChatPinRequest{ExPeer: peer, Message: pos, JustMine: p.JustMine}
		} else {
			method = "UnPinMessages"
			request = &wire.ChatUnpinRequest{ExPeer: peer, Messages: []*wire.MessagePosition{pos}}
		}
	case "message.unpin_all":
		method = "UnPinMessages"
		request = &wire.ChatUnpinRequest{ExPeer: peer, All: true}
	case "message.pins":
		method = "LoadPinnedMessages"
		request = &wire.ChatPeerRequest{Peer: peer}
		response = &wire.ChatPinsResponse{}
	case "folders.list":
		method = "LoadFolders"
		request = &wire.FolderListRequest{IncludeMutedUnreadPeers: p.IncludeMuted}
		response = &wire.FolderListResponse{}
	case "folders.create":
		peers, e := peerList(p.Peers)
		if e != nil {
			return nil, e
		}
		method = "CreateFolder"
		request = &wire.FolderCreateRequest{Name: p.Name, Peers: peers}
		response = &wire.FolderCreateResponse{}
	case "folders.delete", "folders.edit":
		id, e := strconv.ParseInt(p.FolderID, 10, 32)
		if e != nil || id <= 0 {
			return nil, boundedError("INVALID_REQUEST", "folder_id must be a positive int32 string", 400)
		}
		if operation == "folders.delete" {
			method = "DeleteFolder"
			request = &wire.FolderDeleteRequest{FolderId: int32(id)}
		} else {
			added, e := peerList(p.AddedPeers)
			if e != nil {
				return nil, e
			}
			deleted, e := peerList(p.DeletedPeers)
			if e != nil {
				return nil, e
			}
			method = "EditFolder"
			request = &wire.FolderEditRequest{FolderId: int32(id), Name: p.Name, AddedPeers: added, DeletedPeers: deleted}
			response = &wire.FolderEditResponse{}
		}
	default:
		return nil, domains.Unsupported(operation)
	}
	rpc := c.rpc
	if contract.Mode == "read" {
		rpc = c.readRPC
	}
	data, err := rpc(ctx, service, method, request)
	if err != nil {
		return nil, err
	}
	if decode(data, response) != nil {
		if contract.Mode == "mutation" {
			return nil, ambiguous()
		}
		return nil, protocolError()
	}
	switch r := response.(type) {
	case *wire.ChatPinsResponse:
		out := make([]map[string]any, 0, len(r.Messages))
		for _, m := range r.Messages {
			if validateHistoryItem(m) != nil {
				return nil, protocolError()
			}
			out = append(out, map[string]any{"message_id": strconv.FormatInt(m.Rid, 10), "sender_id": strconv.FormatUint(uint64(m.SenderId), 10), "date": time.UnixMilli(m.Date).UTC(), "payload": decoratedHistoryPayload(m, false)})
		}
		return json.Marshal(map[string]any{"messages": out})
	case *wire.FolderListResponse:
		folders := make([]map[string]any, 0, len(r.Folders))
		for _, f := range r.Folders {
			peers, e := safeExPeers(f.Peers)
			if e != nil {
				return nil, e
			}
			unread, e := safeExPeers(f.UnreadPeers)
			if e != nil {
				return nil, e
			}
			folders = append(folders, map[string]any{"id": strconv.FormatInt(int64(f.Id), 10), "name": f.Name, "peers": peers, "unread_peers": unread, "is_reserved": f.IsReserved})
		}
		unread := make([]map[string]any, 0, len(r.UnreadPeers))
		for _, u := range r.UnreadPeers {
			p, e := safeExPeer(u.Peer)
			if e != nil {
				return nil, e
			}
			unread = append(unread, map[string]any{"peer": p, "date": strconv.FormatInt(u.LastMessageDate, 10), "is_muted": u.IsMuted})
		}
		return json.Marshal(map[string]any{"folders": folders, "unread_peers": unread})
	case *wire.FolderCreateResponse:
		if r.FolderId <= 0 {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "folder_id": strconv.FormatInt(int64(r.FolderId), 10), "index": r.Index})
	default:
		return json.RawMessage(`{"acknowledged":true}`), nil
	}
}

func extendedPeer(p *wire.Peer, public domains.Peer) *wire.Peer {
	copy := wire.Peer{Type: p.Type, Id: p.Id, AccessHash: p.AccessHash}
	if public.Type == "channel" {
		copy.Type = 3
	}
	return &copy
}
func safeExPeer(p *wire.Peer) (domains.Peer, error) {
	if p == nil || p.Id == 0 {
		return domains.Peer{}, protocolError()
	}
	kind := ""
	switch p.Type {
	case 1, 4:
		kind = "user"
	case 2, 5:
		kind = "group"
	case 3:
		kind = "channel"
	default:
		return domains.Peer{}, protocolError()
	}
	return domains.Peer{Type: kind, ID: strconv.FormatUint(uint64(p.Id), 10)}, nil
}
func safeExPeers(list []*wire.Peer) ([]domains.Peer, error) {
	out := make([]domains.Peer, 0, len(list))
	for _, p := range list {
		v, e := safeExPeer(p)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
