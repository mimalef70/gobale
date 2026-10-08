package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/mimalef70/gobale/src/domains"
)

// EnqueueUpload commits an owned, already durable file and its outbox request
// together. Failed admission, conflicts and retries leave no media registration,
// so the upload manager can safely clean the unused staging file. The hash binds
// bytes and public metadata, never the randomly allocated staging identifier.
func (s *Store) EnqueueUpload(ctx context.Context, conn string, req domains.SendRequest, key string, media domains.Media, digest string, limits AdmissionLimits) (op domains.Operation, err error) {
	start := time.Now()
	defer func() { s.observePersistence("enqueue", start, err) }()
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || digest != hex.EncodeToString(decoded) || media.ID == "" || media.Path == "" || media.Size <= 0 || media.ConnectionID != conn || req.MediaID != media.ID {
		return op, domains.E("INVALID_MEDIA", "upload identity or digest is invalid", 400)
	}
	canonical := req
	canonical.MediaID = ""
	canonical.RequestID = ""
	body, err := json.Marshal(struct {
		Request     domains.SendRequest `json:"request"`
		Digest      string              `json:"sha256"`
		Name        string              `json:"name"`
		ContentType string              `json:"content_type"`
		Size        int64               `json:"size"`
	}{canonical, digest, media.Name, media.ContentType, media.Size})
	if err != nil {
		return op, err
	}
	hash := sha256.Sum256(body)
	tx, err := s.beginTx(ctx)
	if err != nil {
		return op, err
	}
	defer s.rollbackTx(tx)
	if err = activeTx(ctx, tx, conn); err != nil {
		return op, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO media(id,connection_id,name,content_type,size,path,created_at) VALUES(?,?,?,?,?,?,?)`, media.ID, conn, media.Name, media.ContentType, media.Size, media.Path, now()); err != nil {
		return op, err
	}
	op, created, err := s.enqueueHashedTx(ctx, tx, conn, req, key, limits, hex.EncodeToString(hash[:]))
	if err != nil || !created {
		return op, err
	}
	return op, s.commitTx(tx)
}
