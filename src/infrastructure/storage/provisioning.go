package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mimalef70/goomni/src/domains"
)

const provisioningV7Schema = `CREATE TABLE device_provisioning(idempotency_key TEXT PRIMARY KEY,payload_hash BLOB NOT NULL,connection_id TEXT NOT NULL UNIQUE REFERENCES devices(connection_id),created_at INTEGER NOT NULL)`
const provisioningSchema = `CREATE TABLE device_provisioning(idempotency_key TEXT PRIMARY KEY,payload_hash BLOB NOT NULL,hash_version INTEGER NOT NULL CHECK(hash_version IN (1,2)),connection_id TEXT NOT NULL UNIQUE REFERENCES devices(connection_id),created_at INTEGER NOT NULL)`

// The fingerprint includes the webhook secret. Derive a separate HMAC key so
// reading the database does not permit testing guesses of that secret.
func deriveProvisioningKey(encryptionKey []byte) []byte {
	mac := hmac.New(sha256.New, encryptionKey)
	_, _ = mac.Write([]byte("gobale:device-provisioning-key:v1"))
	return mac.Sum(nil)
}

func (s *Store) provisioningHash(request domains.ProvisionDeviceRequest) ([]byte, error) {
	return s.provisioningHashVersion(request, 2)
}

func (s *Store) provisioningHashVersion(request domains.ProvisionDeviceRequest, version int) ([]byte, error) {
	// Struct serialization fixes field order and omits empty events consistently.
	// Filter fields likewise treat nil and empty sets as the same configuration.
	var body []byte
	var err error
	switch version {
	case 1:
		if request.Provider != domains.ProviderBale {
			return nil, providerMismatch()
		}
		// Exact schema-7 field order/tags: adding Provider to the current request
		// must not invalidate a previously committed provisioning key.
		legacy := struct {
			DeviceID      string                `json:"device_id"`
			WebhookURL    string                `json:"webhook_url,omitempty"`
			WebhookSecret string                `json:"webhook_secret,omitempty"`
			WebhookEvents []string              `json:"webhook_events,omitempty"`
			WebhookFilter domains.WebhookFilter `json:"webhook_filter,omitempty"`
		}{request.DeviceID, request.WebhookURL, request.WebhookSecret, request.WebhookEvents, request.WebhookFilter}
		body, err = json.Marshal(legacy)
	case 2:
		body, err = json.Marshal(request)
	default:
		return nil, invalidPrivateData("provisioning fingerprint")
	}
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, s.provisioningKey)
	if version == 1 {
		_, _ = mac.Write([]byte("gobale:device-provisioning-request:v1\x00"))
	} else {
		_, _ = mac.Write([]byte("gobale:device-provisioning-request:v2\x00"))
	}
	_, _ = mac.Write(body)
	return mac.Sum(nil), nil
}

// ProvisionDevice commits the initial device, webhook and idempotency binding
// together. Keys are global to this store, separate from send/schedule keys, and
// permanently retain their connection identity even after device deletion.
func (s *Store) ProvisionDevice(ctx context.Context, request domains.ProvisionDeviceRequest, key string) (domains.Device, bool, error) {
	var empty domains.Device
	if !validProvider(request.Provider) {
		return empty, false, invalidProvider()
	}
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
	var hashVersion int
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,hash_version,connection_id FROM device_provisioning WHERE idempotency_key=?`, key).Scan(&previousHash, &hashVersion, &connection)
	if err == nil {
		replayHash, hashErr := s.provisioningHashVersion(request, hashVersion)
		if hashErr != nil || !hmac.Equal(replayHash, previousHash) {
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO devices(connection_id,alias,provider,created_at,webhook_url,webhook_secret,webhook_events,webhook_filter,webhook_revision) VALUES(?,?,?,?,?,?,?,?,1)`, connection, request.DeviceID, request.Provider, created, request.WebhookURL, secret, eventsJSON, filterJSON); err != nil {
		return empty, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_provisioning(idempotency_key,payload_hash,hash_version,connection_id,created_at) VALUES(?,?,2,?,?)`, key, hash, connection, created); err != nil {
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
