package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/mimalef70/gobale/src/domains"
)

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

const deviceColumns = `alias,connection_id,account_id,created_at,webhook_url,webhook_secret,webhook_events,webhook_revision`

func (s *Store) scanDevice(row scanner) (d domains.Device, err error) {
	var created int64
	var secret []byte
	var events string
	err = row.Scan(&d.ID, &d.ConnectionID, &d.AccountID, &created, &d.Webhook.URL, &secret, &events, &d.Webhook.Revision)
	if err != nil {
		return d, dbError(err)
	}
	d.CreatedAt = stamp(created)
	if err = json.Unmarshal([]byte(events), &d.Webhook.Events); err != nil {
		return d, err
	}
	if len(secret) > 0 {
		var p []byte
		p, err = s.decrypt(secret, d.ConnectionID+":webhook")
		d.Webhook.Secret = string(p)
	}
	return
}
func (s *Store) CreateDevice(ctx context.Context, id string) (domains.Device, error) {
	if !aliasPattern.MatchString(id) {
		return domains.Device{}, domains.E("INVALID_DEVICE_ID", "device id must contain 1-64 letters, digits, dots, underscores or hyphens", 400)
	}
	d := domains.Device{ID: id, ConnectionID: newID(), CreatedAt: stamp(now()), Webhook: domains.WebhookConfig{Revision: 1, Events: []string{}}}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return d, e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE alias=? AND deleted_at IS NULL`, id).Scan(&n); e != nil {
		return d, e
	}
	if n > 0 {
		return d, domains.E("DEVICE_EXISTS", "device id already exists", 409)
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO devices(connection_id,alias,created_at) VALUES(?,?,?)`, d.ConnectionID, d.ID, d.CreatedAt.UnixMilli())
	if e != nil {
		return d, e
	}
	return d, tx.Commit()
}
func (s *Store) ListDevices(ctx context.Context) ([]domains.Device, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE deleted_at IS NULL ORDER BY created_at,alias`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []domains.Device{}
	for rows.Next() {
		d, e := s.scanDevice(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
func (s *Store) GetDevice(ctx context.Context, id string) (domains.Device, error) {
	return s.scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE alias=? AND deleted_at IS NULL`, id))
}
func (s *Store) DeviceByConnection(ctx context.Context, conn string) (domains.Device, error) {
	return s.scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn))
}
func (s *Store) DeleteDevice(ctx context.Context, conn string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = activeTx(ctx, tx, conn); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM sessions WHERE connection_id=?`, conn); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE operations SET state=CASE WHEN state='sending' THEN 'unknown' ELSE 'cancelled' END,error_code='DEVICE_DELETED',error_message='device deleted',updated_at=? WHERE connection_id=? AND state IN ('queued','sending')`, now(), conn); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE deliveries SET state='cancelled',last_error='device deleted' WHERE connection_id=? AND state IN ('queued','retry','paused','delivering')`, conn); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE schedules SET state='cancelled' WHERE connection_id=? AND state IN ('active','paused')`, conn); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE devices SET deleted_at=?,webhook_secret=NULL WHERE connection_id=?`, now(), conn); e != nil {
		return e
	}
	return tx.Commit()
}
func bindAccountTx(ctx context.Context, tx *sql.Tx, conn, user string) error {
	if user == "" {
		return domains.E("INVALID_SESSION", "session account id is required", 400)
	}
	var existing string
	if e := tx.QueryRowContext(ctx, `SELECT account_id FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn).Scan(&existing); e != nil {
		return dbError(e)
	}
	if existing != "" && existing != user {
		return domains.E("ACCOUNT_CONFLICT", "device is already bound to a different account; create a new device", 409)
	}
	_, e := tx.ExecContext(ctx, `UPDATE devices SET account_id=? WHERE connection_id=?`, user, conn)
	return e
}
func (s *Store) BindAccount(ctx context.Context, conn, user string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = bindAccountTx(ctx, tx, conn, user); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) SaveSession(ctx context.Context, conn string, session *domains.Session) error {
	if session == nil {
		return domains.E("INVALID_SESSION", "session is required", 400)
	}
	plain, e := json.Marshal(session)
	if e != nil {
		return e
	}
	cipher, e := s.encrypt(plain, conn+":session")
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = bindAccountTx(ctx, tx, conn, session.UserID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO sessions(connection_id,cipher) VALUES(?,?) ON CONFLICT(connection_id) DO UPDATE SET cipher=excluded.cipher`, conn, cipher); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) LoadSession(ctx context.Context, conn string) (*domains.Session, error) {
	if e := s.active(ctx, conn); e != nil {
		return nil, e
	}
	var ciphertext []byte
	e := s.db.QueryRowContext(ctx, `SELECT cipher FROM sessions WHERE connection_id=?`, conn).Scan(&ciphertext)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	plain, e := s.decrypt(ciphertext, conn+":session")
	if e != nil {
		return nil, e
	}
	var session domains.Session
	if e = json.Unmarshal(plain, &session); e != nil {
		return nil, e
	}
	return &session, nil
}
func (s *Store) ClearSession(ctx context.Context, conn string) error {
	if e := s.active(ctx, conn); e != nil {
		return e
	}
	_, e := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE connection_id=?`, conn)
	return e
}
func ValidateWebhookURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return domains.E("INVALID_WEBHOOK_URL", "webhook_url must be an HTTP(S) URL without credentials or fragment", 400)
	}
	return nil
}
func (s *Store) PatchWebhook(ctx context.Context, conn string, p domains.WebhookPatch) (domains.WebhookConfig, error) {
	if p.URL != nil {
		if e := ValidateWebhookURL(*p.URL); e != nil {
			return domains.WebhookConfig{}, e
		}
	}
	if p.Secret != nil && len(*p.Secret) > 4096 {
		return domains.WebhookConfig{}, domains.E("INVALID_WEBHOOK_SECRET", "webhook secret exceeds limit", 400)
	}
	if p.Events != nil {
		if len(*p.Events) > 100 {
			return domains.WebhookConfig{}, domains.E("INVALID_EVENT_FILTER", "too many event filters", 400)
		}
		for _, v := range *p.Events {
			if strings.TrimSpace(v) == "" || len(v) > 128 {
				return domains.WebhookConfig{}, domains.E("INVALID_EVENT_FILTER", "invalid event filter", 400)
			}
		}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return domains.WebhookConfig{}, e
	}
	defer tx.Rollback()
	d, e := s.scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn))
	if e != nil {
		return d.Webhook, e
	}
	cfg := d.Webhook
	urlChanged := false
	if p.URL != nil && cfg.URL != *p.URL {
		cfg.URL = *p.URL
		cfg.Revision++
		urlChanged = true
	}
	if p.Secret != nil {
		cfg.Secret = *p.Secret
	}
	if p.Events != nil {
		cfg.Events = *p.Events
	}
	if cfg.Events == nil {
		cfg.Events = []string{}
	}
	cipher, e := s.encrypt([]byte(cfg.Secret), conn+":webhook")
	if e != nil {
		return cfg, e
	}
	events, e := marshal(cfg.Events)
	if e != nil {
		return cfg, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE devices SET webhook_url=?,webhook_secret=?,webhook_events=?,webhook_revision=? WHERE connection_id=?`, cfg.URL, cipher, events, cfg.Revision, conn); e != nil {
		return cfg, e
	}
	if urlChanged {
		if _, e = tx.ExecContext(ctx, `UPDATE deliveries SET state='paused',last_error='webhook URL changed; explicit replay required' WHERE connection_id=? AND state IN ('queued','retry')`, conn); e != nil {
			return cfg, e
		}
	}
	if e = tx.Commit(); e != nil {
		return cfg, e
	}
	return cfg, nil
}
func (s *Store) Checkpoint(ctx context.Context, conn string) (string, error) {
	var cp string
	e := s.db.QueryRowContext(ctx, `SELECT checkpoint FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn).Scan(&cp)
	return cp, dbError(e)
}

// Stats contains low-cardinality operational counters, never account identifiers.
func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	result := map[string]int64{}
	for _, item := range []struct{ name, query string }{
		{"devices", `SELECT COUNT(*) FROM devices WHERE deleted_at IS NULL`},
		{"outbox_queued", `SELECT COUNT(*) FROM operations WHERE state='queued'`},
		{"outbox_unknown", `SELECT COUNT(*) FROM operations WHERE state='unknown'`},
		{"webhook_pending", `SELECT COUNT(*) FROM deliveries WHERE state IN ('queued','retry','delivering')`},
		{"webhook_failed", `SELECT COUNT(*) FROM deliveries WHERE state='failed'`},
		{"webhook_paused", `SELECT COUNT(*) FROM deliveries WHERE state='paused'`},
		{"webhook_cancelled", `SELECT COUNT(*) FROM deliveries WHERE state='cancelled'`},
		{"webhook_delivered", `SELECT COUNT(*) FROM deliveries WHERE state='delivered'`},
		{"events", `SELECT COUNT(*) FROM events`},
	} {
		var n int64
		if e := s.db.QueryRowContext(ctx, item.query).Scan(&n); e != nil {
			return nil, fmt.Errorf("read storage statistics: %w", e)
		}
		result[item.name] = n
	}
	var oldest sql.NullInt64
	if e := s.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM deliveries WHERE state IN ('queued','retry','delivering')`).Scan(&oldest); e != nil {
		return nil, e
	}
	if oldest.Valid {
		age := (now() - oldest.Int64) / 1000
		if age < 0 {
			age = 0
		}
		result["webhook_oldest_pending_age_seconds"] = age
	} else {
		result["webhook_oldest_pending_age_seconds"] = 0
	}
	if e := s.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM operations WHERE state IN ('queued','sending')`).Scan(&oldest); e != nil {
		return nil, e
	}
	if oldest.Valid {
		age := (now() - oldest.Int64) / 1000
		if age < 0 {
			age = 0
		}
		result["outbox_oldest_pending_age_seconds"] = age
	} else {
		result["outbox_oldest_pending_age_seconds"] = 0
	}
	return result, nil
}

// LogoutConnection forgets the local session and atomically stops work accepted
// under it. The immutable account binding and audit records remain. A claimed
// send is conservatively unknown and can never be requeued by a late worker.
func (s *Store) LogoutConnection(ctx context.Context, conn string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = activeTx(ctx, tx, conn); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM sessions WHERE connection_id=?`, conn); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE operations SET state=CASE WHEN state='sending' THEN 'unknown' ELSE 'cancelled' END,error_code='ACCOUNT_LOGGED_OUT',error_message='account logged out before operation completed',updated_at=? WHERE connection_id=? AND state IN ('queued','sending')`, now(), conn); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE schedules SET state='cancelled' WHERE connection_id=? AND state IN ('active','paused')`, conn); e != nil {
		return e
	}
	return tx.Commit()
}
