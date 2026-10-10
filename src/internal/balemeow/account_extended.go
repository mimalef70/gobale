package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const accountUsersService = "bale.users.v1.Users"

var accountNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
var settingKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func decodeAccountBody(raw []byte, dest any) error {
	if !json.Valid(raw) {
		return boundedError("INVALID_REQUEST", "invalid JSON body", 400)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dest)
}

type accountExtendedRequest struct {
	User      domains.Peer   `json:"user"`
	Users     []domains.Peer `json:"users"`
	Full      bool           `json:"full"`
	Name      *string        `json:"name"`
	About     *string        `json:"about"`
	Username  *string        `json:"username"`
	RequestID string         `json:"request_id"`
	Confirm   bool           `json:"confirm"`
	Contacts  []struct {
		Phone string  `json:"phone"`
		Name  *string `json:"name"`
	} `json:"contacts"`
	Type      *int32  `json:"type"`
	Status    *int32  `json:"status"`
	Key       string  `json:"key"`
	Value     *string `json:"value"`
	SessionID string  `json:"session_id"`
	MediaID   string  `json:"media_id"`
}

// Mutations without a provider RID still require a persisted local request ID.
// Their transport errors remain ambiguous; the journal must never retry them.
func requireAccountMutationID(id string) error {
	if _, err := positiveID(id); err != nil {
		return boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before mutation", 400)
	}
	return nil
}
func boundedAccountText(v *string, limit int, empty bool) bool {
	return v != nil && utf8.ValidString(*v) && (empty || strings.TrimSpace(*v) != "") && utf8.RuneCountInString(*v) <= limit
}
func sensitiveSettingKey(key string) bool {
	k := strings.ToLower(key)
	for _, term := range []string{"password", "token", "secret", "credential", "cookie", "auth", "access_hash", "api_key"} {
		if strings.Contains(k, term) {
			return true
		}
	}
	return false
}
func (c *Client) accountUserRef(ctx context.Context, peer domains.Peer) (*wire.PeerRef, error) {
	if peer.AccessHash != "" {
		return nil, boundedError("INVALID_PEER", "caller-supplied access hashes are not accepted", 400)
	}
	return c.resolvedUserRef(ctx, peer)
}
func (c *Client) accountSelfID() (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return 0, boundedError("AUTH_REQUIRED", "authenticate the account first", 401)
	}
	id, err := strconv.ParseUint(c.session.UserID, 10, 32)
	if err != nil || id == 0 {
		return 0, protocolError()
	}
	return uint32(id), nil
}
func accountUserView(u *wire.User) (map[string]any, error) {
	if u == nil || u.Id == 0 || len(u.Name)+len(u.GetLocalName().GetValue())+len(u.GetNick().GetValue()) > 16384 {
		return nil, protocolError()
	}
	return map[string]any{"peer": domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(u.Id), 10)}, "name": u.Name, "local_name": u.GetLocalName().GetValue(), "username": u.GetNick().GetValue(), "is_bot": u.GetIsBot().GetValue(), "is_deleted": u.GetIsDeleted().GetValue()}, nil
}
func accountFullUserView(u *wire.AccountFullUser) (map[string]any, error) {
	if u == nil || u.Id == 0 || len(u.GetAbout().GetValue()) > 16384 || len(u.GetTimezone().GetValue()) > 128 || len(u.Languages) > 32 || len(u.ContactInfo) > 64 {
		return nil, protocolError()
	}
	for _, v := range u.Languages {
		if len(v) > 128 {
			return nil, protocolError()
		}
	}
	return map[string]any{"peer": domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(u.Id), 10)}, "about": u.GetAbout().GetValue(), "timezone": u.GetTimezone().GetValue(), "languages": u.Languages, "is_blocked": u.GetIsBlocked().GetValue(), "is_deleted": u.GetIsDeleted().GetValue(), "is_contact": u.GetIsContact().GetValue()}, nil
}

func (c *Client) accountExtendedCall(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	var p accountExtendedRequest
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > 128<<10 || decodeAccountBody(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid account request", 400)
	}
	if op == "account.info" {
		return c.accountOwnInfo(ctx)
	}
	read := false
	switch op {
	case "users.get", "account.username.check", "account.blocked", "account.sessions", "account.privacy", "account.privacy.status", "account.settings":
		read = true
	}
	if !read {
		if err := requireAccountMutationID(p.RequestID); err != nil {
			return nil, err
		}
	}
	service, method := accountUsersService, ""
	var request proto.Message = &wire.Empty{}
	bad := func() (json.RawMessage, error) {
		return nil, boundedError("INVALID_REQUEST", "invalid or missing account operation field", 400)
	}
	switch op {
	case "account.avatar":
		if p.MediaID == "" || len(p.MediaID) > 128 {
			return bad()
		}
		location, err := c.uploadProfileImage(ctx, p.MediaID)
		if err != nil {
			return nil, err
		}
		method = "EditAvatar"
		request = &wire.AccountAvatarRequest{FileLocation: location}
	case "account.name":
		if !boundedAccountText(p.Name, 128, false) {
			return bad()
		}
		method = "EditName"
		request = &wire.AccountStringRequest{Value: *p.Name}
	case "account.about":
		if !boundedAccountText(p.About, 4096, true) {
			return bad()
		}
		method = "EditAbout"
		request = &wire.AccountWrappedStringRequest{Value: &wire.StringValue{Value: *p.About}}
	case "account.username", "account.username.check":
		if p.Username == nil || (*p.Username != "" && !accountNamePattern.MatchString(*p.Username)) || (read && *p.Username == "") {
			return bad()
		}
		if read {
			method = "CheckNickName"
			request = &wire.AccountStringRequest{Value: *p.Username}
		} else {
			method = "EditNickName"
			request = &wire.AccountWrappedStringRequest{Value: &wire.StringValue{Value: *p.Username}}
		}
	case "contacts.add", "contacts.remove", "contacts.rename", "account.block", "account.unblock":
		if op == "contacts.rename" && !boundedAccountText(p.Name, 128, true) {
			return bad()
		}
		ref, err := c.accountUserRef(ctx, p.User)
		if err != nil {
			return nil, err
		}
		switch op {
		case "contacts.add":
			method = "AddContact"
			request = &wire.AccountContactRequest{Uid: ref.Id, AccessHash: ref.AccessHash}
		case "contacts.remove":
			method = "RemoveContact"
			request = &wire.AccountContactRequest{Uid: ref.Id, AccessHash: ref.AccessHash}
		case "contacts.rename":
			method = "EditUserLocalName"
			request = &wire.AccountLocalNameRequest{Uid: ref.Id, AccessHash: ref.AccessHash, Name: *p.Name}
		case "account.block":
			method = "BlockUser"
			request = &wire.AccountPeerRequest{Peer: ref}
		case "account.unblock":
			method = "UnblockUser"
			request = &wire.AccountPeerRequest{Peer: ref}
		}
	case "contacts.import":
		if len(p.Contacts) < 1 || len(p.Contacts) > 100 {
			return bad()
		}
		q := &wire.AccountImportRequest{}
		seen := map[string]bool{}
		for _, contact := range p.Contacts {
			phone, err := normalizePhone(contact.Phone)
			if err != nil {
				return nil, err
			}
			if seen[phone] {
				return bad()
			}
			seen[phone] = true
			if contact.Name != nil && !boundedAccountText(contact.Name, 128, true) {
				return bad()
			}
			n, _ := strconv.ParseInt(phone, 10, 64)
			v := &wire.AccountContactImport{Phone: n}
			if contact.Name != nil {
				v.Name = &wire.StringValue{Value: *contact.Name}
			}
			q.Phones = append(q.Phones, v)
		}
		method = "ImportContacts"
		request = q
	case "contacts.reset":
		if !p.Confirm {
			return bad()
		}
		method = "ResetContacts"
	case "account.blocked":
		method = "LoadBlockedUsers"
	case "users.get":
		if len(p.Users) < 1 || len(p.Users) > 100 {
			return bad()
		}
		q := &wire.AccountPeersRequest{}
		seen := map[string]bool{}
		// Validate the whole list before reference lookups can issue network reads.
		for _, peer := range p.Users {
			if peer.Type != "user" || peer.AccessHash != "" || seen[peer.ID] {
				return bad()
			}
			if _, err := encodePeer(peer); err != nil {
				return nil, err
			}
			seen[peer.ID] = true
		}
		for _, peer := range p.Users {
			r, err := c.accountUserRef(ctx, peer)
			if err != nil {
				return nil, err
			}
			q.Peers = append(q.Peers, r)
		}
		method = "LoadUsers"
		if p.Full {
			method = "LoadFullUsers"
		}
		request = q
	case "account.privacy", "account.privacy.status", "account.privacy.set":
		if op != "account.privacy" && (p.Type == nil || *p.Type < 0 || *p.Type > 2) {
			return bad()
		}
		if op == "account.privacy.set" && (p.Status == nil || *p.Status < 0 || *p.Status > 2 || p.User.ID != "" || p.User.Type != "" || p.User.AccessHash != "") {
			return bad()
		}
		id, err := c.accountSelfID()
		if err != nil {
			return nil, err
		}
		if p.User.ID != "" || p.User.Type != "" {
			r, err := c.accountUserRef(ctx, p.User)
			if err != nil {
				return nil, err
			}
			id = r.Id
		}
		q := &wire.AccountPrivacyRequest{UserId: id}
		if p.Type != nil {
			q.Type = *p.Type
		}
		if p.Status != nil {
			q.Status = *p.Status
		}
		method = "GetUserFullPrivacy"
		if op == "account.privacy.status" {
			method = "GetUserPrivacyStatus"
		}
		if op == "account.privacy.set" {
			method = "SetUserPrivacyStatus"
		}
		request = q
	case "account.settings":
		service = "bale.v1.Configs"
		method = "GetParameters"
	case "account.settings.set":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if _, ok := fields["value"]; !ok {
			return bad()
		}
		if !settingKeyPattern.MatchString(p.Key) || sensitiveSettingKey(p.Key) || (p.Value != nil && !boundedAccountText(p.Value, 8192, true)) {
			return bad()
		}
		q := &wire.AccountSettingRequest{Key: p.Key}
		if p.Value != nil {
			q.Value = &wire.StringValue{Value: *p.Value}
		}
		service = "bale.v1.Configs"
		method = "EditParameter"
		request = q
	case "account.sessions":
		service = "bale.auth.v1.Auth"
		method = "GetAuthSessions"
	case "account.session.terminate":
		id, err := strconv.ParseInt(p.SessionID, 10, 32)
		if err != nil || id <= 0 {
			return bad()
		}
		// Only terminate a session returned for this authenticated account.
		data, err := c.readRPC(ctx, "bale.auth.v1.Auth", "GetAuthSessions", &wire.Empty{})
		if err != nil {
			return nil, err
		}
		list := &wire.AccountSessionsResponse{}
		if decode(data, list) != nil || len(list.Sessions) > 256 {
			return nil, protocolError()
		}
		found := false
		for _, s := range list.Sessions {
			if s != nil && int64(s.Id) == id {
				found = true
			}
		}
		if !found {
			return nil, boundedError("SESSION_NOT_FOUND", "session is not part of this account", 404)
		}
		service = "bale.auth.v1.Auth"
		method = "TerminateSession"
		request = &wire.AccountTerminateSessionRequest{Id: int32(id)}
	case "account.sessions.terminate":
		if !p.Confirm {
			return bad()
		}
		service = "bale.auth.v1.Auth"
		method = "TerminateAllSessions"
	default:
		return nil, domains.Unsupported(op)
	}
	var data []byte
	var err error
	if read {
		data, err = c.readRPC(ctx, service, method, request)
	} else {
		data, err = c.rpc(ctx, service, method, request)
	}
	if err != nil {
		return nil, err
	}
	malformed := func() (json.RawMessage, error) {
		if read {
			return nil, protocolError()
		}
		return nil, ambiguous()
	}
	switch op {
	case "account.username.check":
		r := &wire.BoolValue{}
		if decode(data, r) != nil {
			return malformed()
		}
		return json.Marshal(map[string]any{"username": *p.Username, "available": r.Value})
	case "account.blocked":
		r := &wire.AccountPeersResponse{}
		if decode(data, r) != nil || len(r.Peers) > 4096 {
			return malformed()
		}
		out := []domains.Peer{}
		for _, v := range r.Peers {
			if v == nil || v.Id == 0 {
				return malformed()
			}
			out = append(out, domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(v.Id), 10)})
		}
		for _, v := range r.Peers {
			c.rememberRef("user", v)
		}
		return json.Marshal(map[string]any{"users": out})
	case "users.get":
		out := []map[string]any{}
		allowed := map[string]bool{}
		for _, u := range p.Users {
			allowed[u.ID] = true
		}
		if p.Full {
			r := &wire.AccountFullUsersResponse{}
			if decode(data, r) != nil || len(r.Users) > len(p.Users) {
				return malformed()
			}
			for _, u := range r.Users {
				v, e := accountFullUserView(u)
				if e != nil || !allowed[strconv.FormatUint(uint64(u.GetId()), 10)] {
					return malformed()
				}
				out = append(out, v)
			}

		} else {
			r := &wire.AccountUsersResponse{}
			if decode(data, r) != nil || len(r.Users) > len(p.Users) {
				return malformed()
			}
			for _, u := range r.Users {
				v, e := accountUserView(u)
				if e != nil || !allowed[strconv.FormatUint(uint64(u.GetId()), 10)] {
					return malformed()
				}
				out = append(out, v)
			}
			for _, u := range r.Users {
				c.rememberRef("user", &wire.PeerRef{Id: u.Id, AccessHash: u.AccessHash})
				c.rememberUserName(u)
			}
		}
		return json.Marshal(map[string]any{"users": out})
	case "contacts.import":
		r := &wire.AccountImportResponse{}
		if decode(data, r) != nil || len(r.Users)+len(r.Peers) > 200 {
			return malformed()
		}
		out := []map[string]any{}
		seen := map[uint32]bool{}
		for _, u := range r.Users {
			v, e := accountUserView(u)
			if e != nil {
				return malformed()
			}
			out = append(out, v)
			seen[u.Id] = true
		}
		for _, v := range r.Peers {
			if v == nil || v.Id == 0 {
				return malformed()
			}
			if !seen[v.Id] {
				out = append(out, map[string]any{"peer": domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(v.Id), 10)}})
			}
		}
		for _, u := range r.Users {
			c.rememberRef("user", &wire.PeerRef{Id: u.Id, AccessHash: u.AccessHash})
			c.rememberUserName(u)
		}
		for _, v := range r.Peers {
			c.rememberRef("user", v)
		}
		return json.Marshal(map[string]any{"acknowledged": true, "contacts": out, "requested_count": len(p.Contacts)})
	case "account.privacy":
		r := &wire.AccountPrivacyResponse{}
		if decode(data, r) != nil || r.Privacy == nil {
			return malformed()
		}
		v := r.Privacy
		if v.Invite < 0 || v.Invite > 2 || v.Presence < 0 || v.Presence > 2 || v.MoneyTransfer < 0 || v.MoneyTransfer > 2 {
			return malformed()
		}
		return json.Marshal(map[string]any{"invite": v.Invite, "presence": v.Presence, "money_transfer": v.MoneyTransfer})
	case "account.privacy.status":
		r := &wire.AccountPrivacyStatus{}
		if decode(data, r) != nil || r.Status < 0 || r.Status > 2 {
			return malformed()
		}
		return json.Marshal(map[string]any{"type": *p.Type, "status": r.Status})
	case "account.settings":
		r := &wire.AccountSettingsResponse{}
		if decode(data, r) != nil || len(r.Parameters) > 4096 {
			return malformed()
		}
		out := map[string]string{}
		omitted := 0
		for _, v := range r.Parameters {
			if v == nil || len(v.Key) > 1024 || len(v.Value) > 32768 {
				return malformed()
			}
			// Account config includes long internal/cache keys that are not
			// editable public settings. Omit them without failing the entire
			// bounded response or advertising opaque provider data.
			if !settingKeyPattern.MatchString(v.Key) || sensitiveSettingKey(v.Key) {
				omitted++
				continue
			}
			out[v.Key] = v.Value
		}
		return json.Marshal(map[string]any{"parameters": out, "redacted_count": omitted})
	case "account.sessions":
		r := &wire.AccountSessionsResponse{}
		if decode(data, r) != nil || len(r.Sessions) > 256 {
			return malformed()
		}
		out := []map[string]any{}
		for _, v := range r.Sessions {
			if v == nil || v.Id <= 0 || len(v.AppTitle)+len(v.DeviceTitle)+len(v.AuthLocation) > 8192 {
				return malformed()
			}
			out = append(out, map[string]any{"session_id": strconv.FormatInt(int64(v.Id), 10), "app_id": strconv.FormatInt(int64(v.AppId), 10), "app_title": v.AppTitle, "device_title": v.DeviceTitle, "created_at_seconds": strconv.FormatInt(int64(v.AuthTime), 10), "last_activity_at": strconv.FormatInt(v.GetLastActivityAt().GetValue(), 10)})
		}
		return json.Marshal(map[string]any{"sessions": out})
	default:
		if decode(data, &wire.Empty{}) != nil {
			return malformed()
		}
		return json.RawMessage(`{"acknowledged":true}`), nil
	}
}

// ResolvePhone never imports contacts or assumes the first search hit is the
// requested account. Only an exact provider-returned phone field is sufficient.
func (c *Client) ResolvePhone(ctx context.Context, phone string) (domains.Peer, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	normalized, err := normalizePhone(phone)
	if err != nil {
		return domains.Peer{}, err
	}
	data, err := c.readRPC(ctx, accountUsersService, "SearchContacts", &wire.ContactsSearchRequest{Request: "+" + normalized})
	if err != nil {
		return domains.Peer{}, err
	}
	r := &wire.AccountSearchResponse{}
	if decode(data, r) != nil || len(r.Users)+len(r.Peers) > 4096 {
		return domains.Peer{}, protocolError()
	}
	var match *wire.PeerRef
	for _, u := range r.Users {
		if u == nil || u.Id == 0 || len(u.ContactInfo) > 64 {
			return domains.Peer{}, protocolError()
		}
		found := false
		for _, v := range u.ContactInfo {
			if v == nil {
				return domains.Peer{}, protocolError()
			}
			if v.Type != 0 {
				continue
			}
			for _, candidate := range []string{v.GetStringValue().GetValue(), strconv.FormatInt(v.GetLongValue().GetValue(), 10)} {
				if len(candidate) > 64 {
					return domains.Peer{}, protocolError()
				}
				if n, e := normalizePhone(candidate); e == nil && n == normalized {
					found = true
				}
			}
		}
		if found {
			if match != nil && match.Id != u.Id {
				return domains.Peer{}, boundedError("PHONE_AMBIGUOUS", "more than one account matched the exact phone", 409)
			}
			match = &wire.PeerRef{Id: u.Id, AccessHash: u.AccessHash}
		}
	}
	if match == nil {
		return domains.Peer{}, boundedError("PHONE_NOT_FOUND", "provider did not return an exact accessible account for this phone", 404)
	}
	for _, p := range r.Peers {
		if p == nil || p.Id == 0 {
			return domains.Peer{}, protocolError()
		}
		if p.Id == match.Id {
			match.AccessHash = p.AccessHash
		}
	}
	c.rememberRef("user", match)
	return domains.Peer{Type: "user", ID: strconv.FormatUint(uint64(match.Id), 10)}, nil
}

func (c *Client) accountOwnInfo(ctx context.Context) (json.RawMessage, error) {
	id, err := c.accountSelfID()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.session == nil || c.session.UserID != strconv.FormatUint(uint64(id), 10) {
		c.mu.Unlock()
		return nil, boundedError("AUTH_REQUIRED", "account session changed", 401)
	}
	phone := c.session.Phone
	hash := c.peerHashes["user:"+strconv.FormatUint(uint64(id), 10)]
	c.mu.Unlock()
	ref := &wire.PeerRef{Id: id, AccessHash: hash}
	data, err := c.readRPC(ctx, accountUsersService, "GetFullUser", &wire.AccountPeerRequest{Peer: ref})
	if err != nil {
		return nil, err
	}
	detail := &wire.AccountFullUserResponse{}
	if decode(data, detail) != nil || detail.User.GetId() != id {
		return nil, protocolError()
	}
	u := detail.User
	basic, err := accountUserView(&wire.User{Id: u.Id, Name: u.Name, LocalName: u.LocalName, Nick: u.Nick, IsBot: u.IsBot, IsDeleted: u.IsDeleted})
	if err != nil {
		return nil, err
	}
	full, err := accountFullUserView(&wire.AccountFullUser{Id: u.Id, About: u.About, Languages: u.Languages, Timezone: u.Timezone, IsBlocked: u.IsBlocked, IsDeleted: u.IsDeleted, IsContact: u.IsContact})
	if err != nil {
		return nil, err
	}
	c.rememberRef("user", &wire.PeerRef{Id: u.Id, AccessHash: u.AccessHash})
	for k, v := range full {
		basic[k] = v
	}
	delete(basic, "peer")
	basic["account_id"] = strconv.FormatUint(uint64(id), 10)
	basic["phone"] = phone
	return json.Marshal(basic)
}
