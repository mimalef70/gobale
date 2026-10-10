package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mimalef70/goomni/src/domains"
)

const immutableProviderTrigger = `CREATE TRIGGER devices_immutable_provider BEFORE UPDATE OF provider ON devices WHEN OLD.provider<>NEW.provider BEGIN SELECT RAISE(ABORT,'connection provider is immutable'); END`
const checkpointSchema = `CREATE TABLE provider_checkpoints(connection_id TEXT NOT NULL REFERENCES devices(connection_id),scope TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(connection_id,scope))`
const dispatchSchema = `CREATE TABLE connection_dispatch(connection_id TEXT PRIMARY KEY REFERENCES devices(connection_id),last_claim INTEGER NOT NULL)`

// The envelope identifies the protocol owner and its private format. Payload is
// encrypted using the original connection-bound AAD; it never becomes API JSON.
type privateEnvelope struct {
	Provider domains.Provider `json:"provider"`
	Version  int              `json:"version"`
	Payload  json.RawMessage  `json:"payload"`
}

func validProvider(provider domains.Provider) bool {
	return provider == domains.ProviderBale || provider == domains.ProviderEitaa || provider == domains.ProviderRubika
}

func providerTx(ctx context.Context, tx *sql.Tx, conn string) (domains.Provider, error) {
	var provider domains.Provider
	err := tx.QueryRowContext(ctx, `SELECT provider FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn).Scan(&provider)
	return provider, dbError(err)
}

func (s *Store) connectionProvider(ctx context.Context, conn string) (domains.Provider, error) {
	var provider domains.Provider
	err := s.db.QueryRowContext(ctx, `SELECT provider FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn).Scan(&provider)
	return provider, dbError(err)
}

func (s *Store) encodePrivate(provider domains.Provider, version int, payload []byte, aad string) ([]byte, error) {
	if !validProvider(provider) || version < 1 || len(payload) == 0 || !json.Valid(payload) {
		return nil, domains.E("INVALID_PROVIDER_DATA", "private provider data has an invalid envelope", 500)
	}
	plain, err := json.Marshal(privateEnvelope{Provider: provider, Version: version, Payload: payload})
	if err != nil {
		return nil, err
	}
	return s.encrypt(plain, aad)
}

func (s *Store) decodePrivate(ciphertext []byte, aad string, provider domains.Provider) (privateEnvelope, error) {
	var envelope privateEnvelope
	plain, err := s.decrypt(ciphertext, aad)
	if err != nil {
		return envelope, err
	}
	if json.Unmarshal(plain, &envelope) != nil || envelope.Provider != provider || !validProvider(provider) || envelope.Version < 1 || len(envelope.Payload) == 0 || !json.Valid(envelope.Payload) {
		return envelope, domains.E("INVALID_PROVIDER_DATA", "private provider data does not match the connection", 500)
	}
	return envelope, nil
}

// All schema-7 connections are Bale, including tombstones. Retain legacy AAD,
// IDs, journal payloads and delivery bytes exactly; wrap only private records.
func (s *Store) migrateProviders(ctx context.Context, tx *sql.Tx) error {
	for _, ddl := range []string{
		`ALTER TABLE devices ADD COLUMN provider TEXT NOT NULL DEFAULT 'bale' CHECK(provider IN ('bale','eitaa','rubika'))`,
		immutableProviderTrigger,
		checkpointSchema,
		dispatchSchema,
		operationStageSchema,
		providerMediaOrderSchema,
		`INSERT INTO provider_checkpoints(connection_id,scope,value) SELECT connection_id,'default',checkpoint FROM devices WHERE checkpoint<>''`,
		`ALTER TABLE device_provisioning ADD COLUMN hash_version INTEGER NOT NULL DEFAULT 1 CHECK(hash_version IN (1,2))`,
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	// Process encrypted rows in bounded batches. A malformed record aborts the
	// same migration transaction instead of discarding sessions or references.
	for _, table := range []string{"sessions", "provider_media"} {
		last := int64(0)
		for {
			query := `SELECT rowid,connection_id,cipher FROM sessions WHERE rowid>? ORDER BY rowid LIMIT 100`
			if table == "provider_media" {
				query = `SELECT rowid,connection_id,peer_key,message_id,cipher FROM provider_media WHERE rowid>? AND cipher IS NOT NULL ORDER BY rowid LIMIT 100`
			}
			rows, err := tx.QueryContext(ctx, query, last)
			if err != nil {
				return err
			}
			type record struct {
				rowid               int64
				conn, peer, message string
				cipher              []byte
			}
			batch := make([]record, 0, 100)
			for rows.Next() {
				var item record
				if table == "sessions" {
					err = rows.Scan(&item.rowid, &item.conn, &item.cipher)
				} else {
					err = rows.Scan(&item.rowid, &item.conn, &item.peer, &item.message, &item.cipher)
				}
				if err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, item)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(batch) == 0 {
				break
			}
			for _, item := range batch {
				aad := item.conn + ":session"
				if table == "provider_media" {
					aad = item.conn + ":provider-media:" + item.peer + ":" + item.message
				}
				plain, err := s.decrypt(item.cipher, aad)
				if err != nil {
					return err
				}
				ciphertext, err := s.encodePrivate(domains.ProviderBale, 1, plain, aad)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET cipher=? WHERE rowid=?`, ciphertext, item.rowid); err != nil {
					return err
				}
				last = item.rowid
			}
		}
	}
	return nil
}

func (s *Store) ScopedCheckpoint(ctx context.Context, conn, scope string) (string, error) {
	if err := validateCheckpointScope(scope); err != nil {
		return "", err
	}
	if err := s.active(ctx, conn); err != nil {
		return "", err
	}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM provider_checkpoints WHERE connection_id=? AND scope=?`, conn, scope).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func checkpointValueTx(ctx context.Context, tx *sql.Tx, conn, scope string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM provider_checkpoints WHERE connection_id=? AND scope=?`, conn, scope).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func setCheckpointTx(ctx context.Context, tx *sql.Tx, conn, scope, value string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_checkpoints(connection_id,scope,value) VALUES(?,?,?) ON CONFLICT(connection_id,scope) DO UPDATE SET value=excluded.value`, conn, scope, value); err != nil {
		return err
	}
	// Keep the original column synchronized while the Bale adapter's load hook
	// and historical database diagnostics still use the default cursor.
	if scope == domains.DefaultCheckpointScope {
		_, err := tx.ExecContext(ctx, `UPDATE devices SET checkpoint=? WHERE connection_id=?`, value, conn)
		return err
	}
	return nil
}

func providerMismatch() error {
	return domains.E("PROVIDER_MISMATCH", "provider data does not belong to the selected connection", 409)
}

func invalidProvider() error {
	return domains.E("INVALID_PROVIDER", "provider must be bale, eitaa or rubika", 400)
}

func invalidPrivateData(kind string) error {
	return domains.E("INVALID_PROVIDER_DATA", fmt.Sprintf("stored %s data is invalid", kind), 500)
}
