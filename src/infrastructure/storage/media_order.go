package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

// Kept separate from the encrypted media envelope so schema-7 ciphertext/AAD
// remain unchanged. Absence of a row means only the old timestamp is known.
const providerMediaOrderSchema = `CREATE TABLE provider_media_order(connection_id TEXT NOT NULL,peer_key TEXT NOT NULL,message_id TEXT NOT NULL,scope TEXT NOT NULL,sequence INTEGER NOT NULL,kind TEXT NOT NULL,PRIMARY KEY(connection_id,peer_key,message_id),FOREIGN KEY(connection_id,peer_key,message_id) REFERENCES provider_media(connection_id,peer_key,message_id))`

func validateMediaRevision(provider domains.Provider, peer domains.Peer, kind string, revision *domains.MediaRevision) error {
	if revision == nil {
		return nil
	}
	invalid := func() error {
		return domains.E("INVALID_MEDIA_REVISION", "attachment ordering evidence is invalid for this provider and peer", 502)
	}
	if provider != domains.ProviderEitaa || revision.Sequence <= 0 || revision.Sequence > 2147483647 || len(revision.Scope) > 256 {
		return invalid()
	}
	wireID := peer.ID
	supergroup := peer.Type == "group" && strings.HasPrefix(peer.ID, "channel_")
	if supergroup {
		wireID = strings.TrimPrefix(peer.ID, "channel_")
	}
	id, err := strconv.ParseInt(wireID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != wireID || peer.AccessHash != "" {
		return invalid()
	}
	if kind != "message" && kind != "message.edited" && kind != "message.deleted" {
		return invalid()
	}
	switch {
	case revision.Scope == "account":
		if (peer.Type != "user" && peer.Type != "group") || supergroup {
			return invalid()
		}
	case strings.HasPrefix(revision.Scope, "channel:"):
		if (peer.Type != "channel" && !supergroup) || revision.Scope != "channel:"+wireID {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

type mediaOrder struct {
	scope    string
	sequence int64
	kind     string
}

// chooseMediaWrite never compares unrelated streams or interprets opaque update
// timestamps as sequence numbers. Conflicting equal-time edits become unavailable
// until genuinely newer ordering evidence arrives, rather than serving old bytes.
func chooseMediaWrite(previous, next mediaOrder, oldTime, newTime int64, same bool) string {
	if next.kind == "history" {
		return "keep"
	}
	if previous.sequence > 0 && next.sequence > 0 {
		if previous.scope != next.scope {
			return "invalidate"
		}
		if next.sequence > previous.sequence {
			return "replace"
		}
		if next.sequence < previous.sequence {
			return "keep"
		}
		if same {
			return "keep"
		}
		return "invalidate"
	}
	// An original message can never supersede a known edit/deletion, even if a
	// provider supplies an inconsistent creation timestamp on a late replay.
	if next.kind == "message" && (previous.kind == "message.edited" || previous.kind == "message.deleted" || previous.kind == "ambiguous") {
		return "keep"
	}
	if newTime > oldTime {
		return "replace"
	}
	if newTime < oldTime {
		return "keep"
	}
	if next.kind == "message.deleted" {
		return "replace"
	}
	if previous.kind == "message.deleted" {
		return "keep"
	}
	// A reviewed edit is later than the original content of that same message,
	// even when the provider gives both a timestamp rounded to one second.
	if next.kind == "message.edited" && previous.kind == "message" {
		return "replace"
	}
	if same {
		return "keep"
	}
	return "invalidate"
}

func (s *Store) writeProviderMediaTx(ctx context.Context, tx *sql.Tx, conn string, provider domains.Provider, peer domains.Peer, messageID string, m *domains.ProviderMedia, ciphertext, plain []byte, eventTime int64, kind string, revision *domains.MediaRevision) (bool, error) {
	next := mediaOrder{kind: kind}
	if revision != nil {
		next.scope, next.sequence = revision.Scope, revision.Sequence
	}
	var previous mediaOrder
	var stored []byte
	var oldTime int64
	err := tx.QueryRowContext(ctx, `SELECT m.cipher,m.event_time,COALESCE(o.scope,''),COALESCE(o.sequence,0),COALESCE(o.kind,'') FROM provider_media m LEFT JOIN provider_media_order o ON o.connection_id=m.connection_id AND o.peer_key=m.peer_key AND o.message_id=m.message_id WHERE m.connection_id=? AND m.peer_key=? AND m.message_id=?`, conn, peer.Key(), messageID).Scan(&stored, &oldTime, &previous.scope, &previous.sequence, &previous.kind)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return false, err
	}
	same := m == nil && len(stored) == 0
	if m != nil && len(stored) > 0 {
		envelope, err := s.decodePrivate(stored, providerMediaAAD(conn, peer, messageID), provider)
		if err != nil {
			return false, err
		}
		same = envelope.Version == m.Version && bytes.Equal(envelope.Payload, plain)
	}
	action := "replace"
	if !missing {
		action = chooseMediaWrite(previous, next, oldTime, eventTime, same)
	}
	if action == "keep" {
		return m != nil && same && len(stored) > 0, nil
	}
	// A newer reviewed sequence may carry an older provider timestamp. Replace
	// the reference, but retain the timestamp high-water mark for subsequent
	// unversioned records. The public event timestamp is never changed.
	if !missing {
		eventTime = max(oldTime, eventTime)
	}
	if action == "invalidate" {
		ciphertext = nil
		if previous.sequence > 0 {
			next.scope, next.sequence = previous.scope, previous.sequence
		}
		next.kind = "ambiguous"
		if kind == "message.deleted" || previous.kind == "message.deleted" {
			next.kind = "message.deleted"
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_media(connection_id,peer_key,message_id,cipher,event_time) VALUES(?,?,?,?,?) ON CONFLICT(connection_id,peer_key,message_id) DO UPDATE SET cipher=excluded.cipher,event_time=excluded.event_time`, conn, peer.Key(), messageID, ciphertext, eventTime)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_media_order(connection_id,peer_key,message_id,scope,sequence,kind) VALUES(?,?,?,?,?,?) ON CONFLICT(connection_id,peer_key,message_id) DO UPDATE SET scope=excluded.scope,sequence=excluded.sequence,kind=excluded.kind`, conn, peer.Key(), messageID, next.scope, next.sequence, next.kind)
	return err == nil && m != nil && action == "replace", err
}
