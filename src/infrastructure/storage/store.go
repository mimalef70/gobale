// Package storage provides GoBale's single-owner, transactional SQLite journal.
package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/pkg/sqlite"
	"golang.org/x/sys/unix"
)

type Store struct {
	db              *sql.DB
	aead            cipher.AEAD
	lock            *os.File
	dbPath          string
	metrics         storageMetrics
	provisioningKey []byte
}
type scanner interface{ Scan(...any) error }

const schemaVersion = 7

func now() int64               { return time.Now().UTC().UnixMilli() }
func stamp(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
func notFound() error          { return domains.E("NOT_FOUND", "resource not found", 404) }
func dbError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFound()
	}
	return err
}
func newID() string { return uuid.NewString() }
func page(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// Open exclusively owns path until Close. A key is mandatory and is never saved
// alongside the database. Existing unrelated or newer databases are refused.
func Open(path string, key []byte) (s *Store, err error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("storage encryption key must contain exactly 32 bytes")
	}
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("a persistent database path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	if resolved, e := filepath.EvalSymlinks(abs); e == nil {
		abs = resolved
	} else if dir, e := filepath.EvalSymlinks(filepath.Dir(abs)); e == nil {
		abs = filepath.Join(dir, filepath.Base(abs))
	}
	lock, err := os.OpenFile(abs+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("database is already owned by another GoBale process: %w", err)
	}
	defer func() {
		if err != nil {
			unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			lock.Close()
		}
	}()
	st, err := lock.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("database must be a regular file")
	}
	dbFile, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	dbInfo, err := dbFile.Stat()
	dbFile.Close()
	if err != nil {
		return nil, err
	}
	if !dbInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("database must be a regular file")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: abs}).String()
	db, err := sql.Open(sqlite.DriverName, sqlite.FormatChatStorageURI(uri, false, true))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s = &Store{db: db, aead: aead, lock: lock, dbPath: abs, provisioningKey: deriveProvisioningKey(key)}
	defer func() {
		if err != nil {
			db.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		return nil, err
	}
	// FULL is intentional for the durable queue, including purego builds whose
	// inherited URI helper selects NORMAL by default.
	if err = s.migrate(ctx); err != nil {
		return nil, err
	}
	if err = os.Chmod(abs, 0600); err != nil {
		return nil, err
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=30000"} {
		if _, err = db.ExecContext(ctx, pragma); err != nil {
			return nil, err
		}
	}
	if _, err = db.ExecContext(ctx, `UPDATE operations SET state='unknown',error_code='PROCESS_INTERRUPTED',error_message='process stopped while the outcome was unknown',updated_at=? WHERE state='sending'`, now()); err != nil {
		return nil, err
	}
	if err = s.reconcileStoredVoiceProofs(ctx); err != nil {
		return nil, err
	}
	if _, err = db.ExecContext(ctx, `UPDATE deliveries SET state='retry',next_at=? WHERE state='delivering'`, now()); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	err := s.db.Close()
	unlock := unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	closeErr := s.lock.Close()
	return errors.Join(err, unlock, closeErr)
}
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) encrypt(plain []byte, aad string) ([]byte, error) {
	n := make([]byte, s.aead.NonceSize())
	if _, e := io.ReadFull(rand.Reader, n); e != nil {
		return nil, e
	}
	return s.aead.Seal(n, n, plain, []byte(aad)), nil
}
func (s *Store) decrypt(blob []byte, aad string) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(blob) < n {
		return nil, fmt.Errorf("invalid encrypted storage record")
	}
	p, e := s.aead.Open(nil, blob[:n], blob[n:], []byte(aad))
	if e != nil {
		return nil, fmt.Errorf("cannot decrypt storage record: wrong key or corrupted data")
	}
	return p, nil
}
func (s *Store) migrate(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&count); e != nil {
		return e
	}
	if count > 0 {
		var identity string
		var version int
		var check []byte
		e = tx.QueryRowContext(ctx, `SELECT identity,version,key_check FROM gobale_meta WHERE id=1`).Scan(&identity, &version, &check)
		if e != nil || identity != "gobale" {
			return fmt.Errorf("refusing unrelated database; GoBale requires its own database")
		}
		if version < 1 || version > schemaVersion {
			return fmt.Errorf("unsupported GoBale schema version %d", version)
		}
		p, e := s.decrypt(check, "gobale-key-check-v1")
		if e != nil || string(p) != "gobale" {
			return fmt.Errorf("storage encryption key does not match this database")
		}
		if version == 1 {
			if _, e = tx.ExecContext(ctx, providerMediaSchema); e != nil {
				return fmt.Errorf("migrate provider media references: %w", e)
			}
		}
		if version <= 2 {
			if _, e = tx.ExecContext(ctx, scheduleIdempotencySchema); e != nil {
				return fmt.Errorf("migrate schedule idempotency: %w", e)
			}
		}
		if version <= 4 {
			// Preserve the previous created_at/rowid ordering during upgrade.
			// Thereafter retries inherit this stable position, not their new rowid.
			for _, ddl := range []string{
				`ALTER TABLE deliveries ADD COLUMN queue_order INTEGER NOT NULL DEFAULT 0`,
				`WITH positions AS MATERIALIZED (SELECT rowid AS source_rowid,ROW_NUMBER() OVER(ORDER BY created_at,rowid) AS position FROM deliveries) UPDATE deliveries SET queue_order=(SELECT position FROM positions WHERE source_rowid=deliveries.rowid)`,
				deliveryQueueOrderIndex,
				storedMessageProofIndex,
				`DROP INDEX IF EXISTS deliveries_pending_order`,
			} {
				if _, e = tx.ExecContext(ctx, ddl); e != nil {
					return fmt.Errorf("migrate stable delivery order: %w", e)
				}
			}
			for _, ddl := range deliveryActiveIndexes {
				if _, e = tx.ExecContext(ctx, ddl); e != nil {
					return fmt.Errorf("migrate active delivery indexes: %w", e)
				}
			}
		}
		if version <= 5 {
			for _, ddl := range apiFeatureMigration {
				if _, e = tx.ExecContext(ctx, ddl); e != nil {
					return fmt.Errorf("migrate API features: %w", e)
				}
			}
		}
		if version <= 6 {
			if _, e = tx.ExecContext(ctx, provisioningSchema); e != nil {
				return fmt.Errorf("migrate device provisioning: %w", e)
			}
		}
		if _, e = tx.ExecContext(ctx, `UPDATE gobale_meta SET version=? WHERE id=1`, schemaVersion); e != nil {
			return e
		}
		return tx.Commit()
	}
	for _, ddl := range schema {
		if _, e = tx.ExecContext(ctx, ddl); e != nil {
			return fmt.Errorf("initialize GoBale schema: %w", e)
		}
	}
	check, e := s.encrypt([]byte("gobale"), "gobale-key-check-v1")
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO gobale_meta(id,identity,version,key_check) VALUES(1,'gobale',?,?)`, schemaVersion, check); e != nil {
		return e
	}
	return tx.Commit()
}

var schema = []string{
	`CREATE TABLE gobale_meta(id INTEGER PRIMARY KEY CHECK(id=1),identity TEXT NOT NULL,version INTEGER NOT NULL,key_check BLOB NOT NULL)`,
	`CREATE TABLE devices(connection_id TEXT PRIMARY KEY,alias TEXT NOT NULL,account_id TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,deleted_at INTEGER,webhook_url TEXT NOT NULL DEFAULT '',webhook_secret BLOB,webhook_events TEXT NOT NULL DEFAULT '[]',webhook_filter TEXT NOT NULL DEFAULT '{}',webhook_revision INTEGER NOT NULL DEFAULT 1,checkpoint TEXT NOT NULL DEFAULT '')`,
	`CREATE UNIQUE INDEX devices_live_alias ON devices(alias) WHERE deleted_at IS NULL`,
	`CREATE TABLE sessions(connection_id TEXT PRIMARY KEY REFERENCES devices(connection_id),cipher BLOB NOT NULL)`,
	`CREATE TABLE operations(id TEXT PRIMARY KEY,connection_id TEXT NOT NULL REFERENCES devices(connection_id),request TEXT NOT NULL,idempotency_key TEXT,payload_hash TEXT NOT NULL,state TEXT NOT NULL,result TEXT,error_code TEXT NOT NULL DEFAULT '',error_message TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,queue_order INTEGER NOT NULL DEFAULT 0,UNIQUE(connection_id,idempotency_key))`,
	`CREATE INDEX operations_queue ON operations(state,created_at)`,
	`CREATE INDEX operations_connection ON operations(connection_id,created_at)`,
	`CREATE TABLE events(id TEXT NOT NULL,connection_id TEXT NOT NULL REFERENCES devices(connection_id),peer_key TEXT NOT NULL,type TEXT NOT NULL,message_id TEXT NOT NULL,event_time INTEGER NOT NULL,body TEXT NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(connection_id,id))`,
	`CREATE INDEX events_chat ON events(connection_id,peer_key,event_time DESC)`,
	`CREATE TABLE deliveries(id TEXT PRIMARY KEY,connection_id TEXT NOT NULL REFERENCES devices(connection_id),event_id TEXT NOT NULL,url TEXT NOT NULL,secret BLOB NOT NULL,device_config INTEGER NOT NULL,revision INTEGER NOT NULL,body TEXT NOT NULL,state TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL,last_error TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,queue_order INTEGER NOT NULL DEFAULT 0,FOREIGN KEY(connection_id,event_id) REFERENCES events(connection_id,id))`,
	`CREATE INDEX deliveries_queue ON deliveries(state,next_at)`,
	`CREATE INDEX deliveries_connection ON deliveries(connection_id,created_at)`,
	`CREATE TABLE schedules(id TEXT PRIMARY KEY,connection_id TEXT NOT NULL REFERENCES devices(connection_id),request TEXT NOT NULL,state TEXT NOT NULL,next_at INTEGER NOT NULL,occurrence_count INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL)`,
	`CREATE INDEX schedules_due ON schedules(state,next_at)`,
	`CREATE TABLE media(id TEXT PRIMARY KEY,connection_id TEXT NOT NULL REFERENCES devices(connection_id),name TEXT NOT NULL,content_type TEXT NOT NULL,size INTEGER NOT NULL,path TEXT NOT NULL,created_at INTEGER NOT NULL)`,
	providerMediaSchema,
	scheduleIdempotencySchema,
	deliveryActiveIndexes[0],
	deliveryActiveIndexes[1],
	deliveryQueueOrderIndex,
	storedMessageProofIndex,
	operationQueueOrderIndex,
	operationPendingIndex,
	operationInflightIndex,
	operationScopeIndex,
	scheduleScopeIndex,
	occurrenceSchema,
	eventOrderIndex,
	mediaPathIndex,
	provisioningSchema,
}

func (s *Store) active(ctx context.Context, conn string) error {
	var n int
	return dbError(s.db.QueryRowContext(ctx, `SELECT 1 FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn).Scan(&n))
}
func activeTx(ctx context.Context, tx *sql.Tx, conn string) error {
	var n int
	return dbError(tx.QueryRowContext(ctx, `SELECT 1 FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn).Scan(&n))
}
func marshal(v any) (string, error) { p, e := json.Marshal(v); return string(p), e }
