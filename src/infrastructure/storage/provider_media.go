package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/mimalef70/gobale/src/domains"
)

const providerMediaSchema = `CREATE TABLE provider_media(connection_id TEXT NOT NULL REFERENCES devices(connection_id),peer_key TEXT NOT NULL,message_id TEXT NOT NULL,cipher BLOB,event_time INTEGER NOT NULL,PRIMARY KEY(connection_id,peer_key,message_id))`

type privateProviderMedia struct {
	FileID      string `json:"file_id"`
	AccessHash  string `json:"access_hash"`
	Size        int64  `json:"size"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
}

func validateProviderMediaKey(peer domains.Peer, messageID string) error {
	if e := peer.Validate(); e != nil {
		return e
	}
	id, e := strconv.ParseInt(messageID, 10, 64)
	if e != nil || id == 0 || len(messageID) == 0 || messageID[0] == '+' {
		return domains.E("INVALID_MESSAGE_ID", "a nonzero signed decimal message id is required", 400)
	}
	return nil
}
func validateProviderMedia(m domains.ProviderMedia) error {
	id, e := strconv.ParseInt(m.FileID, 10, 64)
	if e != nil || id == 0 {
		return domains.E("INVALID_PROVIDER_MEDIA", "invalid provider file id", 502)
	}
	if _, e = strconv.ParseInt(m.AccessHash, 10, 64); e != nil {
		return domains.E("INVALID_PROVIDER_MEDIA", "invalid provider media access reference", 502)
	}
	if m.Size < 0 || len(m.Name) > 4096 || len(m.ContentType) > 255 {
		return domains.E("INVALID_PROVIDER_MEDIA", "invalid provider media metadata", 502)
	}
	return nil
}
func providerMediaAAD(conn string, peer domains.Peer, messageID string) string {
	return conn + ":provider-media:" + peer.Key() + ":" + messageID
}
func (s *Store) saveProviderMediaTx(ctx context.Context, tx *sql.Tx, conn string, peer domains.Peer, messageID string, m *domains.ProviderMedia, eventTime int64) error {
	if e := validateProviderMediaKey(peer, messageID); e != nil {
		return e
	}
	var ciphertext []byte
	if m != nil {
		if e := validateProviderMedia(*m); e != nil {
			return e
		}
		private := privateProviderMedia{FileID: m.FileID, AccessHash: m.AccessHash, Size: m.Size, Name: m.Name, ContentType: m.ContentType}
		plain, e := json.Marshal(private)
		if e != nil {
			return e
		}
		ciphertext, e = s.encrypt(plain, providerMediaAAD(conn, peer, messageID))
		if e != nil {
			return e
		}
	}
	// Nil cipher is a tombstone. A late original message/history record cannot
	// resurrect an attachment that a newer edit or delete already removed.
	_, e := tx.ExecContext(ctx, `INSERT INTO provider_media(connection_id,peer_key,message_id,cipher,event_time) VALUES(?,?,?,?,?) ON CONFLICT(connection_id,peer_key,message_id) DO UPDATE SET cipher=excluded.cipher,event_time=excluded.event_time WHERE excluded.event_time>provider_media.event_time`, conn, peer.Key(), messageID, ciphertext, eventTime)
	return e
}

// SaveProviderMedia registers references returned by this connection's history.
// It must not be exposed as an API accepting caller-supplied hashes. History
// without an update timestamp only initializes absent references (event_time=0),
// so later history reads cannot overwrite edits or deletion tombstones.
func (s *Store) SaveProviderMedia(ctx context.Context, conn string, peer domains.Peer, messageID string, m domains.ProviderMedia) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = activeTx(ctx, tx, conn); e != nil {
		return e
	}
	if e = s.saveProviderMediaTx(ctx, tx, conn, peer, messageID, &m, 0); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) GetProviderMedia(ctx context.Context, conn string, peer domains.Peer, messageID string) (domains.ProviderMedia, error) {
	if e := validateProviderMediaKey(peer, messageID); e != nil {
		return domains.ProviderMedia{}, e
	}
	var ciphertext []byte
	e := s.db.QueryRowContext(ctx, `SELECT m.cipher FROM provider_media m JOIN devices d ON d.connection_id=m.connection_id WHERE m.connection_id=? AND m.peer_key=? AND m.message_id=? AND d.deleted_at IS NULL`, conn, peer.Key(), messageID).Scan(&ciphertext)
	if e != nil {
		return domains.ProviderMedia{}, dbError(e)
	}
	if len(ciphertext) == 0 {
		return domains.ProviderMedia{}, notFound()
	}
	plain, e := s.decrypt(ciphertext, providerMediaAAD(conn, peer, messageID))
	if e != nil {
		return domains.ProviderMedia{}, e
	}
	var m privateProviderMedia
	if e = json.Unmarshal(plain, &m); e != nil {
		return domains.ProviderMedia{}, e
	}
	return domains.ProviderMedia{FileID: m.FileID, AccessHash: m.AccessHash, Size: m.Size, Name: m.Name, ContentType: m.ContentType}, nil
}
