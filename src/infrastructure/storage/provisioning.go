package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mimalef70/gobale/src/domains"
)

const provisioningSchema = `CREATE TABLE device_provisioning(idempotency_key TEXT PRIMARY KEY,payload_hash BLOB NOT NULL,connection_id TEXT NOT NULL UNIQUE REFERENCES devices(connection_id),created_at INTEGER NOT NULL)`

// The fingerprint includes the webhook secret. Derive a separate HMAC key so
// reading the database does not permit testing guesses of that secret.
func deriveProvisioningKey(encryptionKey []byte) []byte {
	mac := hmac.New(sha256.New, encryptionKey)
	_, _ = mac.Write([]byte("gobale:device-provisioning-key:v1"))
	return mac.Sum(nil)
}

func (s *Store) provisioningHash(request domains.ProvisionDeviceRequest) ([]byte, error) {
	// Struct serialization fixes field order and omits empty events consistently.
	// Filter fields likewise treat nil and empty sets as the same configuration.
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, s.provisioningKey)
	_, _ = mac.Write([]byte("gobale:device-provisioning-request:v1\x00"))
	_, _ = mac.Write(body)
	return mac.Sum(nil), nil
}

// ProvisionDevice commits the initial device, webhook and idempotency binding
// together. Keys are global to this store, separate from send/schedule keys, and
// permanently retain their connection identity even after device deletion.
func (s *Store) ProvisionDevice(ctx context.Context, request domains.ProvisionDeviceRequest, key string) (domains.Device, bool, error) {
	var empty domains.Device
	if err := domains.ValidateProvisioningKey(key); err != nil {
		return empty, false, err
	}
	if err := request.Validate(); err != nil {
		return empty, false, err
	}
	hash, err := s.provisioningHash(request)
	if err != nil {
		return empty, false, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return empty, false, err
	}
	defer s.rollbackTx(tx)
	var previousHash []byte
	var connection string
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,connection_id FROM device_provisioning WHERE idempotency_key=?`, key).Scan(&previousHash, &connection)
	if err == nil {
		if !hmac.Equal(hash, previousHash) {
			return empty, false, domains.E("IDEMPOTENCY_CONFLICT", "idempotency key was already used for a different provisioning request", 409)
		}
		var deleted sql.NullInt64
		if err = tx.QueryRowContext(ctx, `SELECT deleted_at FROM devices WHERE connection_id=?`, connection).Scan(&deleted); err != nil {
			return empty, false, err
		}
		if deleted.Valid {
			return empty, false, domains.E("PROVISIONING_RETIRED", "the originally provisioned device has been deleted; use a new provisioning key", 409)
		}
		device, err := s.scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE connection_id=? AND deleted_at IS NULL`, connection))
		if err != nil {
			return empty, false, err
		}
		if err = s.commitTx(tx); err != nil {
			return empty, false, err
		}
		return device, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE alias=? AND deleted_at IS NULL`, request.DeviceID).Scan(&count); err != nil {
		return empty, false, err
	}
	if count > 0 {
		return empty, false, domains.E("DEVICE_EXISTS", "device id already exists", 409)
	}
	connection = newID()
	created := now()
	secret, err := s.encrypt([]byte(request.WebhookSecret), connection+":webhook")
	if err != nil {
		return empty, false, err
	}
	events := request.WebhookEvents
	if events == nil {
		events = []string{}
	}
	eventsJSON, err := marshal(events)
	if err != nil {
		return empty, false, err
	}
	filterJSON, err := marshal(request.WebhookFilter)
	if err != nil {
		return empty, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO devices(connection_id,alias,created_at,webhook_url,webhook_secret,webhook_events,webhook_filter,webhook_revision) VALUES(?,?,?,?,?,?,?,1)`, connection, request.DeviceID, created, request.WebhookURL, secret, eventsJSON, filterJSON); err != nil {
		return empty, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_provisioning(idempotency_key,payload_hash,connection_id,created_at) VALUES(?,?,?,?)`, key, hash, connection, created); err != nil {
		return empty, false, err
	}
	device, err := s.scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE connection_id=?`, connection))
	if err != nil {
		return empty, false, err
	}
	if err = s.commitTx(tx); err != nil {
		return empty, false, err
	}
	return device, false, nil
}
