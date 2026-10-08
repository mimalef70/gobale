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
	name := safeDisplayName(u.Name, u.GetLocalName().GetValue())
	// An omitted or unsafe bulk observation is not a completed name lookup.
	// Negative caching it would suppress a later verified profile read.
	if name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cacheSenderNameLocked(uid(u.Id), name)
}

func (c *Client) cacheSenderNameLocked(id, name string) {
	if c.senderNames == nil {
		c.senderNames = make(map[string]senderName)
	}
	now := time.Now()
	// Partial contact/dialog users can omit a name. They are not evidence that
	// an existing display label disappeared, and must not erase a fresh value.
	if cached := c.senderNames[id]; name == "" && cached.name != "" && now.Before(cached.expires) {
		return
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
	c.senderNames[id] = senderName{name: name, expires: now.Add(ttl)}
}

func (c *Client) cachedSenderDisplayName(id string) (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.senderNames[id]; ok && time.Now().Before(cached.expires) && cached.name != "" {
		return cached.name, "available"
	}
	return "", "unavailable"
}

// Enrichment runs on the bounded, ordered update consumer, separately from the
// websocket reader. A cold sender waits for the next per-client lookup slot
// before the event reaches its durable sink; throttling never drops the lookup.
// Contact/dialog/profile reads then share a 500ms deadline. Waiting is bounded
// and stops on cancellation or socket closure, including during event draining.
func (c *Client) senderDisplayName(ctx context.Context, id string) (string, string) {
	if !domains.CanonicalUserID(id) {
		return "", "unavailable"
	}
	waiting, stopWaiting := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer stopWaiting()
	var conn *connection
	var refreshContacts bool
	for {
		c.mu.Lock()
		now := time.Now()
		if conn != nil && c.conn != conn {
			c.mu.Unlock()
			return "", "unavailable"
		}
		if cached, ok := c.senderNames[id]; ok && now.Before(cached.expires) {
			c.mu.Unlock()
			if cached.name == "" {
				return "", "unavailable"
			}
			return cached.name, "available"
		}
		if waiting.Err() != nil || c.session == nil || c.conn == nil || c.status.Transport != "connected" || c.conn.ctx.Err() != nil {
			c.mu.Unlock()
			return "", "unavailable"
		}
		conn = c.conn
		wait := c.senderLookupAfter.Sub(now)
		if wait > 0 {
			c.mu.Unlock()
			// A real slot is never more than one second away. This also avoids
			// waiting on an invalid deadline outside the limiter's own bound.
			if wait > time.Second {
				return "", "unavailable"
			}
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-waiting.Done():
				timer.Stop()
				return "", "unavailable"
			case <-conn.ctx.Done():
				timer.Stop()
				return "", "unavailable"
			}
			continue // Recheck cache, identity and slot after waiting without locks.
		}
		_, known := c.peerHashes["user:"+id]
		refreshContacts = !known && id != conn.account && !now.Before(c.senderContactsAfter)
		if refreshContacts {
			c.senderContactsAfter = now.Add(30 * time.Second)
		}
		c.senderLookupAfter = now.Add(time.Second)
		c.mu.Unlock()
		break
	}
	lookup, cancel := context.WithTimeout(waiting, 500*time.Millisecond)
	stopOnClose := context.AfterFunc(conn.ctx, cancel)
	defer stopOnClose()
	defer cancel()
	name := c.lookupSenderName(lookup, id, conn.account, refreshContacts)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != conn || c.session == nil || c.session.UserID != conn.account || c.session.Token != conn.token || conn.ctx.Err() != nil {
		return "", "unavailable"
	}
	// Do not erase a valid name another reviewed read populated during this
	// lookup, or negative-cache an event that only waited for a rate-limit slot.
	if cached, ok := c.senderNames[id]; ok && time.Now().Before(cached.expires) && cached.name != "" {
		return cached.name, "available"
	}
	c.cacheSenderNameLocked(id, name)
	if name == "" {
		return "", "unavailable"
	}
	return name, "available"
}

func (c *Client) lookupSenderName(ctx context.Context, id, account string, refreshContacts bool) string {
	if refreshContacts {
		if _, err := c.contacts(ctx, "contacts.list", nil); err != nil {
			return ""
		}
		c.mu.Lock()
		c.senderContactsAfter = time.Now().Add(15 * time.Minute)
		c.mu.Unlock()
	}
	if name, status := c.cachedSenderDisplayName(id); status == "available" {
		return name
	}
	numeric, _ := strconv.ParseUint(id, 10, 32)
	peer := domains.Peer{Type: "user", ID: id}
	ref, known := c.cachedUserRef(peer, uint32(numeric))
	if !known {
		if id == account {
			// The bound authenticated account proves its own zero-hash reference.
			ref = &wire.PeerRef{Id: uint32(numeric)}
		} else {
			var err error
			ref, err = c.dialogUserRef(ctx, peer, uint32(numeric))
			if err != nil {
				return ""
			}
			if name, status := c.cachedSenderDisplayName(id); status == "available" {
				return name
			}
		}
	}
	data, err := c.readRPC(ctx, accountUsersService, "GetFullUser", &wire.GetUserInfoRequest{Peer: ref})
	if err != nil {
		return ""
	}
	r := &wire.GetUserInfoResponse{}
	if decode(data, r) != nil || r.User == nil || r.User.Id != uint32(numeric) {
		return ""
	}
	return safeDisplayName(r.User.Name, r.User.GetLocalName().GetValue())
}
