package rubikameow

import (
	"encoding/json"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
)

var guidPattern = regexp.MustCompile(`^[ugcbs]0[A-Za-z0-9]{1,126}$`)

type Contract struct{ Enabled bool }

func (c Contract) Descriptor() domains.ProviderDescriptor {
	return domains.ProviderDescriptor{ID: domains.ProviderRubika, Name: "Rubika", Enabled: c.Enabled, Verification: "offline; live-unverified", Interactions: domains.ProviderInteractions{Typing: &domains.OperationPreset{Operation: "chat.activity", Parameters: json.RawMessage(`{"activity":"Typing"}`)}, ReadOperation: "message.read", ReadArgument: "message_id", ReceiptModel: "chat_state", SenderNames: "unavailable", AvatarDownload: true, PartialEdits: true}, DeliveryMethods: []string{"sms", "app", "call"}, Send: domains.ProviderSendCapabilities{Kinds: []string{"text", "file", "image", "audio", "voice", "video"}, MaxTextCharacters: 4200, MentionsSupported: false, MaxMentions: 0, ReplySupported: true, MediaFormatNotes: domains.ProviderMediaFormatNotes{Voice: "Complete single-stream Ogg Opus, mapping 0 mono/stereo; inspected without transcoding.", Audio: "Complete MPEG layer III (MP3), at least one second, with duration inspected from frames; Ogg Opus uses voice.", Video: "Nonfragmented MP4 with one AVC video track and a decodable first IDR frame; real thumbnail, dimensions and millisecond timing are derived without transcoding."}}}
}
func (Contract) ValidateUserID(id string) bool { return guidPattern.MatchString(id) && id[0] == 'u' }
func (Contract) SupportsPeerType(kind string) bool {
	return kind == "user" || kind == "group" || kind == "channel" || kind == "bot" || kind == "service"
}
func (Contract) ValidateSenderID(id string) bool {
	return guidPattern.MatchString(id) && (id[0] == 'u' || id[0] == 'b' || id[0] == 's')
}
func (Contract) ValidatePeer(p domains.Peer) error {
	prefix := map[string]byte{"user": 'u', "group": 'g', "channel": 'c', "bot": 'b', "service": 's'}
	if !guidPattern.MatchString(p.ID) || prefix[p.Type] != p.ID[0] || p.AccessHash != "" {
		return domains.E("INVALID_PEER", "Rubika peer type must match its account GUID", 400)
	}
	return nil
}
func (c Contract) ValidateSend(r domains.SendRequest) error {
	if e := c.ValidatePeer(r.Peer); e != nil {
		return e
	}
	if r.Phone != "" {
		return domains.Unsupported("phone destination")
	}
	media := r.Kind == "file" || r.Kind == "image" || r.Kind == "voice" || r.Kind == "audio" || r.Kind == "video"
	if r.Kind != "" && r.Kind != "text" && !media {
		return domains.Unsupported("Rubika media type")
	}
	if media && !domains.ValidOpaqueID(r.MediaID) {
		return domains.E("MEDIA_REQUIRED", "media ID is required", 400)
	}
	if !media && r.MediaID != "" {
		return domains.E("INVALID_REQUEST", "text cannot include media", 400)
	}
	if (!media && r.Text == "") || !utf8.ValidString(r.Text) || len([]rune(r.Text)) > 4200 {
		return domains.E("INVALID_REQUEST", "Rubika text must contain 1 to 4200 characters", 400)
	}
	if len(r.Mentions) > 0 {
		return domains.Unsupported("Rubika mentions")
	}
	if r.ReplyMessageID != "" && !validMessageID(r.ReplyMessageID) {
		return domains.E("INVALID_MESSAGE_ID", "message ID must be a positive int64 string", 400)
	}
	return nil
}
func (Contract) ValidateSession(s *domains.Session) error { _, e := decodeSession(s); return e }
func decodeSession(s *domains.Session) (privateSession, error) {
	var p privateSession
	if s == nil || s.Provider != domains.ProviderRubika || s.Version != 1 || len(s.Data) > 8192 || json.Unmarshal(s.Data, &p) != nil || p.UserID != s.UserID || !(Contract{}).ValidateUserID(p.UserID) {
		return p, domains.E("INVALID_SESSION", "stored Rubika session is invalid", 500)
	}
	if _, e := authKey(p.Auth); e != nil {
		return p, domains.E("INVALID_SESSION", "stored Rubika authentication is invalid", 500)
	}
	if _, e := parsePrivateKey(p.PrivateKey); e != nil {
		return p, domains.E("INVALID_SESSION", "stored Rubika signing key is invalid", 500)
	}
	return p, nil
}
func validMessageID(s string) bool {
	n, e := strconv.ParseInt(s, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == s
}
func textField(n int) domains.FieldSchema { return domains.FieldSchema{Type: "string", MaxLength: n} }
func idField() domains.FieldSchema {
	return domains.FieldSchema{Type: "string", MinLength: 1, MaxLength: 128}
}
func peerField() domains.FieldSchema {
	return domains.FieldSchema{Type: "object", Properties: map[string]domains.FieldSchema{"type": {Type: "string", Enum: []json.RawMessage{json.RawMessage(`"user"`), json.RawMessage(`"group"`), json.RawMessage(`"channel"`), json.RawMessage(`"bot"`), json.RawMessage(`"service"`)}}, "id": idField()}, Required: []string{"type", "id"}}
}
func (Contract) Operations() []domains.OperationContract {
	list := []domains.OperationContract{}
	add := func(name, mode, path string, p map[string]domains.FieldSchema, required ...string) {
		method := "POST"
		path = "/operations/" + name
		list = append(list, domains.OperationContract{Operation: name, Schedulable: name == "message.forward", Mode: mode, Path: path, Method: method, Description: "Rubika " + name, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: p, Required: required}})
	}
	for _, name := range []string{"account.info", "contacts.list", "folders.list", "sticker.list"} {
		add(name, "read", "/provider/"+name, map[string]domains.FieldSchema{})
	}
	for _, name := range []string{"chat.list", "group.list", "channel.list"} {
		add(name, "read", "/provider/"+name, map[string]domains.FieldSchema{"start_id": textField(256)})
	}
	for _, name := range []string{"chat.history", "chat.messages"} {
		add(name, "read", "/provider/"+name, map[string]domains.FieldSchema{"peer": peerField(), "offset_id": idField(), "limit": {Type: "integer"}}, "peer")
	}
	add("chat.info", "read", "", map[string]domains.FieldSchema{"peer": peerField()}, "peer")
	for _, name := range []string{"group.info", "group.link", "group.members", "group.admins", "group.banned", "users.get"} {
		add(name, "read", "/provider/"+name, map[string]domains.FieldSchema{"peer": peerField(), "start_id": textField(256)}, "peer")
	}
	add("message.edit", "mutation", "/provider/message.edit", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField(), "message": textField(16800)}, "peer", "message_id", "message")
	add("message.delete", "mutation", "/provider/message.delete", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField(), "just_mine": {Type: "boolean"}}, "peer", "message_id", "just_mine")
	add("message.forward", "mutation", "/provider/message.forward", map[string]domains.FieldSchema{"peer": peerField(), "source_peer": peerField(), "message_id": idField()}, "peer", "source_peer", "message_id")
	for _, name := range []string{"message.read", "message.pin", "message.unpin"} {
		add(name, "mutation", "/provider/"+name, map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField()}, "peer", "message_id")
	}
	add("message.reaction", "mutation", "/provider/message.reaction", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField(), "reaction_id": {Type: "string", MinLength: 1, MaxLength: 5, Pattern: "^[1-9][0-9]{0,4}$"}}, "peer", "message_id", "reaction_id")
	add("message.reaction.remove", "mutation", "/provider/message.reaction.remove", map[string]domains.FieldSchema{"peer": peerField(), "message_id": idField()}, "peer", "message_id")
	for _, name := range []string{"group.ban", "group.unban"} {
		add(name, "mutation", "/provider/"+name, map[string]domains.FieldSchema{"peer": peerField(), "user_id": idField()}, "peer", "user_id")
	}
	for _, name := range []string{"contacts.block", "contacts.unblock"} {
		add(name, "mutation", "/provider/"+name, map[string]domains.FieldSchema{"user_id": idField()}, "user_id")
	}
	return append(list, extendedOperations()...)
}
func (c Contract) NormalizeOperation(name string, raw json.RawMessage) (json.RawMessage, domains.Peer, error) {
	for _, op := range c.Operations() {
		if op.Operation != name {
			continue
		}
		out, peer, e := domains.NormalizeOperationContract(op, raw)
		if e != nil {
			return nil, peer, e
		}
		var p callRequest
		if json.Unmarshal(out, &p) != nil {
			return nil, peer, domains.E("INVALID_REQUEST", "invalid operation payload", 400)
		}
		if peer.ID != "" {
			if e = c.ValidatePeer(peer); e != nil {
				return nil, peer, e
			}
		}
		if p.SourcePeer.ID != "" {
			if e = c.ValidatePeer(p.SourcePeer); e != nil {
				return nil, peer, e
			}
		}
		if p.UserID != "" && !c.ValidateUserID(p.UserID) {
			return nil, peer, domains.E("INVALID_USER_ID", "Rubika user GUID is required", 400)
		}
		if p.MessageID != "" && !validMessageID(p.MessageID) || p.OffsetID != "" && !validMessageID(p.OffsetID) || p.Limit < 0 || p.Limit > 100 {
			return nil, peer, domains.E("INVALID_REQUEST", "invalid message ID or limit", 400)
		}
		if name == "message.edit" && (p.Text == "" || len([]rune(p.Text)) > 4200) {
			return nil, peer, domains.E("INVALID_REQUEST", "Rubika text must contain 1 to 4200 characters", 400)
		}
		if p.ReactionID != "" && !validMessageID(p.ReactionID) {
			return nil, peer, domains.E("INVALID_REQUEST", "invalid reaction ID", 400)
		}
		if len(name) >= 6 && name[:6] == "group." && name != "group.list" && peer.ID != "" && peer.Type != "group" && peer.Type != "channel" {
			return nil, peer, domains.E("INVALID_PEER", "group or channel peer is required", 400)
		}
		if e = normalizeExtended(name, out); e != nil {
			return nil, peer, e
		}
		return out, peer, nil
	}
	return nil, domains.Peer{}, domains.Unsupported(name)
}
