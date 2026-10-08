package balemeow

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

const maxSenderNames = 4096

type senderName struct {
	name    string
	expires time.Time
}

func safeDisplayName(name, local string) string {
	if strings.TrimSpace(local) != "" {
		name = local
	}
	if !utf8.ValidString(name) || len(name) > 4096 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ""
	}
	return strings.TrimSpace(name)
}

func (c *Client) rememberUserName(u *wire.User) {
	if u == nil || u.Id == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cacheSenderNameLocked(uid(u.Id), safeDisplayName(u.Name, u.GetLocalName().GetValue()))
}

func (c *Client) cacheSenderNameLocked(id, name string) {
	if c.senderNames == nil {
		c.senderNames = make(map[string]senderName)
	}
	if _, exists := c.senderNames[id]; !exists && len(c.senderNames) >= maxSenderNames {
		for old := range c.senderNames {
			delete(c.senderNames, old)
			break
		}
	}
	ttl := 15 * time.Minute
	if name == "" {
		ttl = 30 * time.Second
	}
	c.senderNames[id] = senderName{name: name, expires: time.Now().Add(ttl)}
}

func (c *Client) cachedSenderDisplayName(id string) (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.senderNames[id]; ok && time.Now().Before(cached.expires) && cached.name != "" {
		return cached.name, "available"
	}
	return "", "unavailable"
}

// Enrichment runs on the bounded update consumer, independently of websocket
// response routing. At most one enrichment attempt per second per client may
// refresh contacts and then read a verified reference, sharing a 500ms ceiling.
// Contacts refresh at most every 15 minutes (30 seconds after failure). Failures
// never discard the event. All caches belong to this client and clear on logout.
func (c *Client) senderDisplayName(ctx context.Context, id string) (string, string) {
	if !domains.CanonicalUserID(id) {
		return "", "unavailable"
	}
	c.mu.Lock()
	now := time.Now()
	if cached, ok := c.senderNames[id]; ok && now.Before(cached.expires) {
		c.mu.Unlock()
		if cached.name == "" {
			return "", "unavailable"
		}
		return cached.name, "available"
	}
	hash, known := c.peerHashes["user:"+id]
	if c.session == nil || c.status.Transport != "connected" || now.Before(c.senderLookupAfter) {
		c.mu.Unlock()
		return "", "unavailable"
	}
	account, token := c.session.UserID, c.session.Token
	// The authenticated account is an identity proof for its own profile;
	// provider GetFullUser accepts the self reference with a zero access hash.
	if id == account && !known {
		hash, known = 0, true
	}
	refreshContacts := !known && id != account && !now.Before(c.senderContactsAfter)
	if !known && !refreshContacts {
		c.mu.Unlock()
		return "", "unavailable"
	}
	if refreshContacts {
		c.senderContactsAfter = now.Add(30 * time.Second)
	}
	c.senderLookupAfter = now.Add(time.Second)
	c.cacheSenderNameLocked(id, "")
	c.mu.Unlock()
	lookup, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if refreshContacts {
		if _, err := c.contacts(lookup, "contacts.list", nil); err != nil {
			return "", "unavailable"
		}
		c.mu.Lock()
		if c.session == nil || c.session.UserID != account || c.session.Token != token {
			c.mu.Unlock()
			return "", "unavailable"
		}
		c.senderContactsAfter = time.Now().Add(15 * time.Minute)
		hash, known = c.peerHashes["user:"+id]
		cached := c.senderNames[id]
		c.mu.Unlock()
		if cached.name != "" {
			return cached.name, "available"
		}
		if !known {
			return "", "unavailable"
		}
	}
	numeric, _ := strconv.ParseUint(id, 10, 32)
	data, err := c.readRPC(lookup, accountUsersService, "GetFullUser", &wire.GetUserInfoRequest{Peer: &wire.PeerRef{Id: uint32(numeric), AccessHash: hash}})
	if err != nil {
		return "", "unavailable"
	}
	r := &wire.GetUserInfoResponse{}
	if decode(data, r) != nil || r.User == nil || r.User.Id != uint32(numeric) {
		return "", "unavailable"
	}
	name := safeDisplayName(r.User.Name, r.User.GetLocalName().GetValue())
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil || c.session.UserID != account || c.session.Token != token {
		return "", "unavailable"
	}
	c.cacheSenderNameLocked(id, name)
	if name == "" {
		return "", "unavailable"
	}
	return name, "available"
}
