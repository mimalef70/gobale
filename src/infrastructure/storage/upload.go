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
	hash, err := uploadRequestHash(conn, req, media, digest)
	if err != nil {
		return op, err
	}
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
	op, created, err := s.enqueueHashedTx(ctx, tx, conn, req, key, limits, hash)
	if err != nil || !created {
		return op, err
	}
	return op, s.commitTx(tx)
}

func uploadRequestHash(conn string, req domains.SendRequest, media domains.Media, digest string) (string, error) {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || digest != hex.EncodeToString(decoded) || media.ID == "" || media.Path == "" || media.Size <= 0 || media.ConnectionID != conn || req.MediaID != media.ID {
		return "", domains.E("INVALID_MEDIA", "upload identity or digest is invalid", 400)
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
		return "", err
	}
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:]), nil
}

// LookupScheduleUpload recognizes retries even after the due date or completion.
func (s *Store) LookupScheduleUpload(ctx context.Context, conn string, req domains.SendRequest, key string, media domains.Media, digest string) (domains.Schedule, bool, error) {
	hash, err := uploadRequestHash(conn, req, media, digest)
	if err != nil {
		return domains.Schedule{}, false, err
	}
	if err = s.active(ctx, conn); err != nil {
		return domains.Schedule{}, false, err
	}
	return lookupScheduleHash(ctx, s.db, conn, hash, key)
}

// CreateScheduleUpload registers the file and its retaining schedule together.
// Duplicate uploads roll back their temporary registration; the caller removes
// the unused file. Schedule and immediate-send keys share one namespace.
func (s *Store) CreateScheduleUpload(ctx context.Context, conn string, req domains.SendRequest, next time.Time, key string, media domains.Media, digest string) (job domains.Schedule, err error) {
	start := time.Now()
	defer func() { s.observePersistence("create_schedule", start, err) }()
	hash, err := uploadRequestHash(conn, req, media, digest)
	if err != nil {
		return job, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return job, err
	}
	defer s.rollbackTx(tx)
	if err = activeTx(ctx, tx, conn); err != nil {
		return job, err
	}
	if existing, found, err := lookupScheduleHash(ctx, tx, conn, hash, key); err != nil || found {
		return existing, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO media(id,connection_id,name,content_type,size,path,created_at) VALUES(?,?,?,?,?,?,?)`, media.ID, conn, media.Name, media.ContentType, media.Size, media.Path, now()); err != nil {
		return job, err
	}
	job, err = s.createScheduleTx(ctx, tx, conn, req, next, key, hash)
	if err != nil {
		return job, err
	}
	return job, s.commitTx(tx)
}
