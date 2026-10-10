package eitaameow

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
)

// Contract advertises only operations implemented by this adapter. The schema
// catalog deliberately remains larger than the gateway capability set.
type Contract struct{ Enabled bool }

func (c Contract) Descriptor() domains.ProviderDescriptor {
	return domains.ProviderDescriptor{ID: domains.ProviderEitaa, Name: "Eitaa", Enabled: c.Enabled, Verification: "offline; live-unverified", Interactions: domains.ProviderInteractions{ReadOperation: "message.read", ReadArgument: "message_id", ReceiptModel: "message_id_watermark", ReadEvents: true, SenderNames: "unavailable", AvatarDownload: true}, DeliveryMethods: []string{"sms", "call", "flash_call", "app"}, Send: domains.ProviderSendCapabilities{Kinds: []string{"text", "file", "image", "audio", "voice", "video"}, MaxTextCharacters: 4096, MentionsSupported: false, MaxMentions: 0, ReplySupported: true, MediaFormatNotes: domains.ProviderMediaFormatNotes{Voice: "Complete single-stream Ogg Opus, mapping 0 mono/stereo; inspected without transcoding.", Audio: "Complete single-stream Ogg Opus; send other codecs as files.", Video: "Nonfragmented AVC MP4 with inspected dimensions/timing and an optional real first-frame JPEG preview; no transcoding or playback guarantee. The provider can return short clips as files; inspect received media.type."}}}
}
func (Contract) ValidateUserID(s string) bool {
	n, e := strconv.ParseInt(s, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == s
}
func (c Contract) ValidatePeer(p domains.Peer) error {
	if !c.ValidateUserID(peerWireID(p)) || (p.Type != "user" && p.Type != "group" && p.Type != "channel") || p.AccessHash != "" {
		return domains.E("INVALID_PEER", "Eitaa peer requires a canonical provider ID; channel-backed groups use channel_<decimal>", 400)
	}
	return nil
}
func (c Contract) ValidateSend(r domains.SendRequest) error {
	if err := c.ValidatePeer(r.Peer); err != nil {
		return err
	}
	if r.Phone != "" {
		return domains.Unsupported("phone destination")
	}
	if r.Kind != "" && r.Kind != "text" && r.Kind != "file" && r.Kind != "image" && r.Kind != "voice" && r.Kind != "audio" && r.Kind != "video" {
		return domains.Unsupported("Eitaa media send")
	}
	if ((r.Kind == "" || r.Kind == "text") && r.Text == "") || !utf8.ValidString(r.Text) || len([]rune(r.Text)) > 4096 {
		return domains.E("INVALID_REQUEST", "Eitaa text must contain 1 to 4096 characters", 400)
	}
	if r.Kind != "" && r.Kind != "text" && r.MediaID == "" {
		return domains.E("INVALID_REQUEST", "media_id is required", 400)
	}
	if len(r.Mentions) > 0 {
		return domains.Unsupported("Eitaa mentions")
	}
	if r.ReplyMessageID != "" {
		if _, e := messageNumber(r.ReplyMessageID); e != nil {
			return e
		}
	}
	return nil
}
func (Contract) ValidateSession(s *domains.Session) error { _, e := decodeSession(s); return e }
func decodeSession(s *domains.Session) (privateSession, error) {
	bad := domains.E("INVALID_SESSION", "stored Eitaa session is invalid", 500)
	if s == nil || s.Provider != domains.ProviderEitaa || s.Version != sessionVersion || len(s.Data) > 2<<20 {
		return privateSession{}, bad
	}
	d := json.NewDecoder(bytes.NewReader(s.Data))
	d.DisallowUnknownFields()
	d.UseNumber()
	var p privateSession
	if d.Decode(&p) != nil || p.Token == "" || len(p.Token) > 8192 || len(p.IMEI) != 21 || p.UserID != s.UserID || !(Contract{}).ValidateUserID(p.UserID) || len(p.Peers) > 6000 {
		return privateSession{}, bad
	}
	if d.Decode(new(any)) != io.EOF {
		return privateSession{}, bad
	}
	if p.Peers == nil {
		p.Peers = map[string]object{}
	}
	for key, ref := range p.Peers {
		typ, id, ok := strings.Cut(key, ":")
		peer := domains.Peer{Type: typ, ID: id}
		if !ok || (Contract{}).ValidatePeer(peer) != nil || !peerReferenceMatches(peer, ref) {
			return privateSession{}, bad
		}
	}
	return p, nil
}
func textField(n int) domains.FieldSchema { return domains.FieldSchema{Type: "string", MaxLength: n} }
func idField() domains.FieldSchema {
	return domains.FieldSchema{Type: "string", MinLength: 1, MaxLength: 19, Pattern: "^[1-9][0-9]*$"}
}
func peerField() domains.FieldSchema {
	return domains.FieldSchema{Type: "object", Properties: map[string]domains.FieldSchema{"type": {Type: "string", Enum: []json.RawMessage{json.RawMessage(`"user"`), json.RawMessage(`"group"`), json.RawMessage(`"channel"`)}}, "id": {Type: "string", MinLength: 1, MaxLength: 27, Pattern: "^(channel_)?[1-9][0-9]*$"}}, Required: []string{"type", "id"}}
}
func (Contract) Operations() []domains.OperationContract {
	list := []domains.OperationContract{}
	add := func(name, mode, path string, props map[string]domains.FieldSchema, required ...string) {
		method := "POST"
		if mode == "read" {
			method = "GET"
		}
		list = append(list, domains.OperationContract{Operation: name, Mode: mode, Method: method, Path: path, Description: "Eitaa " + name, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: props, Required: required}})
	}
	add("account.info", "read", "/user/info", map[string]domains.FieldSchema{})
	for _, name := range []string{"chat.list", "group.list", "channel.list"} {
		add(name, "read", "/chats", map[string]domains.FieldSchema{"limit": {Type: "integer"}, "cursor": textField(2048)})
	}
	add("contacts.list", "read", "/user/contacts", map[string]domains.FieldSchema{})
	add("contacts.search", "read", "/user/contacts/search", map[string]domains.FieldSchema{"query": textField(512), "limit": {Type: "integer"}}, "query")
	for _, name := range []string{"chat.history", "chat.messages"} {
		add(name, "read", "/chat/:jid/messages", map[string]domains.FieldSchema{"peer": peerField(), "limit": {Type: "integer"}, "cursor": textField(2048)}, "peer")
	}
	add("message.edit", "mutation", "/message/edit", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField(), "message": textField(16384)}, "peer", "message_id", "message")
	add("message.delete", "mutation", "/message/delete", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField(), "just_mine": {Type: "boolean"}}, "peer", "message_id", "just_mine")
	add("message.forward", "mutation", "/message/forward", map[string]domains.FieldSchema{"peer": peerField(), "source_peer": peerField(), "message_id": idField(), "hide_sender": {Type: "boolean"}}, "peer", "source_peer", "message_id")
	add("message.read", "mutation", "/message/read", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField()}, "peer", "message_id")
	list = append(list, contentSendOperations()...)
	list = append(list, extendedOperations()...)
	list = append(list, moreOperations()...)
	list = append(list, profileOperations()...)
	list = append(list, readOperations()...)
	list = append(list, assetOperations()...)
	list = append(list, adminOperations()...)
	list = append(list, moderationOperations()...)
	for i := range list {
		list[i].Method, list[i].Path = "POST", "/operations/"+list[i].Operation
		switch list[i].Operation {
		case "message.forward", "message.album", "send.poll":
			list[i].Schedulable = true
		}
	}
	return list
}
func (c Contract) NormalizeOperation(name string, raw json.RawMessage) (json.RawMessage, domains.Peer, error) {
	for _, op := range c.Operations() {
		if op.Operation != name {
			continue
		}
		out, peer, err := domains.NormalizeOperationContract(op, raw)
		if err != nil {
			return nil, peer, err
		}
		var p callRequest
		if err = json.Unmarshal(out, &p); err != nil {
			return nil, peer, err
		}
		if peer.ID != "" {
			if err = c.ValidatePeer(peer); err != nil {
				return nil, peer, err
			}
		}
		if p.SourcePeer.ID != "" {
			if err = c.ValidatePeer(p.SourcePeer); err != nil {
				return nil, peer, err
			}
		}
		if p.MessageID != "" {
			if _, err = messageNumber(p.MessageID); err != nil {
				return nil, peer, err
			}
		}
		if p.Limit < 0 || p.Limit > 100 || p.OffsetDate < 0 {
			return nil, peer, domains.E("INVALID_REQUEST", "limit must be between 1 and 100 and offset_date nonnegative", 400)
		}
		if p.OffsetID != "" {
			if _, err = messageNumber(p.OffsetID); err != nil {
				return nil, peer, err
			}
		}
		if name == "message.edit" && (p.Text == "" || len([]rune(p.Text)) > 4096) {
			return nil, peer, domains.E("INVALID_REQUEST", "Eitaa text must contain 1 to 4096 characters", 400)
		}
		if err = normalizeContentSend(name, out); err != nil {
			return nil, peer, err
		}
		if err = normalizeAdmin(name, out); err != nil {
			return nil, peer, err
		}
		if err = normalizeModeration(name, out); err != nil {
			return nil, peer, err
		}
		if err = normalizeAssets(name, out); err != nil {
			return nil, peer, err
		}
		if err = normalizeMore(name, out); err != nil {
			return nil, peer, err
		}
		if err = normalizeExtended(name, out); err != nil {
			return nil, peer, err
		}
		return out, peer, nil
	}
	return nil, domains.Peer{}, domains.Unsupported(name)
}
func messageNumber(s string) (int64, error) {
	n, e := strconv.ParseInt(s, 10, 32)
	if e != nil || n <= 0 || strconv.FormatInt(n, 10) != s {
		return 0, domains.E("INVALID_MESSAGE_ID", "Eitaa message ID must be a positive int32 string", 400)
	}
	return n, nil
}
