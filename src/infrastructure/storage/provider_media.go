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

// The durable event and delivery body must agree with the private reference
// accepted in their transaction. A quote's attachment belongs to another
// message, so only the current content chain may advertise this registration.
func setEventMediaAvailability(event *domains.Event, available bool) error {
	fileID := ""
	if event.Media != nil {
		fileID = event.Media.FileID
	}
	if event.Message != nil && event.Message.Media != nil {
		message, media := *event.Message, *event.Message.Media
		media.DownloadSupported = available && fileID != "" && media.FileID == fileID
		message.Media = &media
		event.Message = &message
	}
	payload, err := setContentMediaAvailability(event.Payload, fileID, available, 0)
	if err != nil {
		return err
	}
	event.Payload = payload
	return nil
}

func setContentMediaAvailability(raw json.RawMessage, fileID string, available bool, depth int) (json.RawMessage, error) {
	if len(raw) == 0 || depth >= 8 {
		return raw, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var kind string
	_ = json.Unmarshal(body["kind"], &kind)
	changed := false
	if kind == "document" {
		var advertised string
		_ = json.Unmarshal(body["file_id"], &advertised)
		body["download_supported"] = json.RawMessage("false")
		if available && fileID != "" && advertised == fileID {
			body["download_supported"] = json.RawMessage("true")
		}
		changed = true
	}
	if kind == "template" || kind == "template_response" {
		if content, ok := body["content"]; ok {
			updated, err := setContentMediaAvailability(content, fileID, available, depth+1)
			if err != nil {
				return nil, err
			}
			body["content"] = updated
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(body)
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
func (s *Store) saveProviderMediaTx(ctx context.Context, tx *sql.Tx, conn string, peer domains.Peer, messageID string, m *domains.ProviderMedia, eventTime int64) (bool, error) {
	if e := validateProviderMediaKey(peer, messageID); e != nil {
		return false, e
	}
	var ciphertext []byte
	if m != nil {
		if e := validateProviderMedia(*m); e != nil {
			return false, e
		}
		private := privateProviderMedia{FileID: m.FileID, AccessHash: m.AccessHash, Size: m.Size, Name: m.Name, ContentType: m.ContentType}
		plain, e := json.Marshal(private)
		if e != nil {
			return false, e
		}
		ciphertext, e = s.encrypt(plain, providerMediaAAD(conn, peer, messageID))
		if e != nil {
			return false, e
		}
	}
	// Nil cipher is a tombstone. A late original message/history record cannot
	// resurrect an attachment that a newer edit or delete already removed.
	_, e := tx.ExecContext(ctx, `INSERT INTO provider_media(connection_id,peer_key,message_id,cipher,event_time) VALUES(?,?,?,?,?) ON CONFLICT(connection_id,peer_key,message_id) DO UPDATE SET cipher=excluded.cipher,event_time=excluded.event_time WHERE excluded.event_time>provider_media.event_time`, conn, peer.Key(), messageID, ciphertext, eventTime)
	if e != nil || m == nil {
		return false, e
	}
	// A successful write can still be a no-op: stale history/updates must not
	// overwrite a newer attachment or deletion tombstone. Advertise only the
	// exact reference that remains accessible under this message's identity.
	var stored []byte
	if e = tx.QueryRowContext(ctx, `SELECT cipher FROM provider_media WHERE connection_id=? AND peer_key=? AND message_id=?`, conn, peer.Key(), messageID).Scan(&stored); e != nil {
		return false, e
	}
	if len(stored) == 0 {
		return false, nil
	}
	plain, e := s.decrypt(stored, providerMediaAAD(conn, peer, messageID))
	if e != nil {
		return false, e
	}
	var actual privateProviderMedia
	if e = json.Unmarshal(plain, &actual); e != nil {
		return false, e
	}
	return actual == (privateProviderMedia{FileID: m.FileID, AccessHash: m.AccessHash, Size: m.Size, Name: m.Name, ContentType: m.ContentType}), nil
}

// SaveProviderMedia registers references returned by this connection's history.
// It must not be exposed as an API accepting caller-supplied hashes. History
// without an update timestamp only initializes absent references (event_time=0),
// so later history reads cannot overwrite edits or deletion tombstones. The bool
// reports whether this exact reference is now available, including an identical
// existing reference. Persistence errors never report availability.
func (s *Store) SaveProviderMedia(ctx context.Context, conn string, peer domains.Peer, messageID string, m domains.ProviderMedia) (bool, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if e = activeTx(ctx, tx, conn); e != nil {
		return false, e
	}
	available, e := s.saveProviderMediaTx(ctx, tx, conn, peer, messageID, &m, 0)
	if e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	return available, nil
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
