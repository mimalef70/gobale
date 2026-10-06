package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mimalef70/gobale/src/domains"
)

// WebhookTarget is a routing snapshot. Device targets fetch the latest secret
// when claimed; global targets retain an encrypted secret snapshot.
type WebhookTarget struct {
	URL      string
	Secret   string
	Revision int64
	Device   bool
}
type Chat struct {
	Peer      domains.Peer  `json:"peer"`
	LastEvent domains.Event `json:"last_event"`
	Count     int           `json:"count"`
}

func scopedEventID(conn string, e domains.Event) string {
	source := e.ID
	if source == "" {
		e.SessionID = ""
		e.AccountID = ""
		e.Checkpoint = ""
		b, _ := json.Marshal(e)
		source = string(b)
	}
	sum := sha256.Sum256([]byte(conn + "\x00" + source))
	return hex.EncodeToString(sum[:])
}

// queueOrder is zero for new work/replay; retries inherit their original queue
// position while keeping a separate attempt record and creation timestamp.
func (s *Store) insertDelivery(ctx context.Context, tx *sql.Tx, conn, eventID, body string, t WebhookTarget, config domains.WebhookConfig, queueOrder int64) (domains.Delivery, error) {
	if e := ValidateWebhookURL(t.URL); e != nil {
		return domains.Delivery{}, e
	}
	if t.URL == "" {
		return domains.Delivery{}, domains.E("INVALID_WEBHOOK_URL", "delivery requires a webhook URL", 400)
	}
	if !t.Device {
		t.Revision = config.Revision
	}
	id := newID()
	cipher, e := s.encrypt([]byte(t.Secret), id+":delivery")
	if e != nil {
		return domains.Delivery{}, e
	}
	state := "queued"
	lastError := ""
	if t.Device && (t.URL != config.URL || t.Revision != config.Revision) {
		state = "paused"
		lastError = "webhook configuration changed before event persistence"
	}
	created := now()
	_, e = tx.ExecContext(ctx, `INSERT INTO deliveries(id,connection_id,event_id,url,secret,device_config,revision,body,state,next_at,last_error,created_at,queue_order) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,CASE WHEN ?=0 THEN (SELECT COALESCE(MAX(queue_order),0)+1 FROM deliveries) ELSE ? END)`, id, conn, eventID, t.URL, cipher, t.Device, t.Revision, body, state, created, lastError, created, queueOrder, queueOrder)
	return domains.Delivery{Device: t.Device, ID: id, ConnectionID: conn, EventID: eventID, URL: t.URL, Secret: t.Secret, Revision: t.Revision, Body: json.RawMessage(body), State: state, NextAt: stamp(created), LastError: lastError, CreatedAt: stamp(created)}, e
}

// AppendEvent atomically writes the event and delivery ledger before advancing
// its checkpoint. Duplicate events neither enqueue duplicate deliveries nor
// rewind a checkpoint. Caller ordering defines opaque provider cursor ordering.
func (s *Store) AppendEvent(ctx context.Context, conn string, event domains.Event, targets []WebhookTarget) (bool, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	d, e := s.scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn))
	if e != nil {
		return false, e
	}
	event.ID = scopedEventID(conn, event)
	event.AccountID = d.AccountID
	event.SessionID = d.ID
	if event.Time.IsZero() {
		event.Time = stamp(now())
	}
	body, e := marshal(event)
	if e != nil {
		return false, e
	}
	r, e := tx.ExecContext(ctx, `INSERT INTO events(id,connection_id,peer_key,type,message_id,event_time,body,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(connection_id,id) DO NOTHING`, event.ID, conn, event.Peer.Key(), event.Type, event.MessageID, event.Time.UnixMilli(), body, now())
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return false, e
	}
	if n == 0 {
		// A replay can restore an older unresolved operation, but only the
		// previously persisted body is evidence. A changed duplicate must not
		// introduce new proof, deliveries, media references or checkpoints.
		var storedBody string
		if e = tx.QueryRowContext(ctx, `SELECT body FROM events WHERE connection_id=? AND id=?`, conn, event.ID).Scan(&storedBody); e != nil {
			return false, e
		}
		storedEvent, err := storedMessageProof(storedBody)
		if err != nil {
			return false, err
		}
		if _, e = s.reconcileOwnMessageTx(ctx, tx, conn, d.AccountID, storedEvent); e != nil {
			return false, e
		}
		return false, tx.Commit()
	}
	switch event.Type {
	case "message", "message.edited":
		if (event.Media != nil || event.Type == "message.edited") && event.MessageID != "" && event.Peer.ID != "" {
			if e = s.saveProviderMediaTx(ctx, tx, conn, event.Peer, event.MessageID, event.Media, event.Time.UnixMilli()); e != nil {
				return false, e
			}
		}
	case "message.deleted":
		if e = s.saveProviderMediaTx(ctx, tx, conn, event.Peer, event.MessageID, nil, event.Time.UnixMilli()); e != nil {
			return false, e
		}
	}
	if _, e = s.reconcileOwnMessageTx(ctx, tx, conn, d.AccountID, event); e != nil {
		return false, e
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target.URL] {
			continue
		}
		seen[target.URL] = true
		if _, e = s.insertDelivery(ctx, tx, conn, event.ID, body, target, d.Webhook, 0); e != nil {
			return false, e
		}
	}
	if event.Checkpoint != "" {
		if _, e = tx.ExecContext(ctx, `UPDATE devices SET checkpoint=? WHERE connection_id=?`, event.Checkpoint, conn); e != nil {
			return false, e
		}
	}
	return true, tx.Commit()
}

// A native own-message echo proves acceptance even when the RPC response was
// lost. Bale preserves the send RID as message ID. Reconcile only that exact
// identity; matching text, dates or an unbound/foreign sender is not evidence.
// Only ordinary sends and the reviewed message-producing operations below can
// be proved by this echo. Other mutations and terminal decisions never change.
func (s *Store) reconcileOwnMessageTx(ctx context.Context, tx *sql.Tx, conn, account string, event domains.Event) (bool, error) {
	if event.Type != "message" || event.Direction != "outgoing" || account == "" || event.AccountID != account || event.SenderID != account || event.MessageID == "" || event.Peer.Validate() != nil || event.Time.UnixMilli() <= 0 {
		return false, nil
	}
	result, err := marshal(domains.SendResult{MessageID: event.MessageID, Date: event.Time})
	if err != nil {
		return false, err
	}
	r, err := tx.ExecContext(ctx, `UPDATE operations SET state='succeeded',result=?,error_code='',error_message='',updated_at=?
 WHERE connection_id=? AND state IN ('sending','unknown')
 AND json_extract(request,'$.request_id')=?
 AND json_extract(request,'$.peer.type')=? AND json_extract(request,'$.peer.id')=?
 AND ((COALESCE(json_extract(request,'$.operation'),'')=''
       AND COALESCE(json_extract(request,'$.kind'),'') IN ('','text','image','file','audio','video','voice'))
      OR (json_extract(request,'$.kind')='operation'
       AND json_extract(request,'$.operation') IN ('send.poll','send.sticker','send.contact','send.location','send.template')))`, result, now(), conn, event.MessageID, event.Peer.Type, event.Peer.ID)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *Store) ListEvents(ctx context.Context, conn, peerKey string, limit, offset int) ([]domains.Event, error) {
	if e := s.active(ctx, conn); e != nil {
		return nil, e
	}
	limit, offset = page(limit, offset)
	query := `SELECT body FROM events WHERE connection_id=?`
	args := []any{conn}
	if peerKey != "" {
		query += ` AND peer_key=?`
		args = append(args, peerKey)
	}
	query += ` ORDER BY event_time DESC,id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, e := s.db.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []domains.Event{}
	for rows.Next() {
		var body string
		if e = rows.Scan(&body); e != nil {
			return nil, e
		}
		var event domains.Event
		if e = json.Unmarshal([]byte(body), &event); e != nil {
			return nil, e
		}
		result = append(result, event)
	}
	return result, rows.Err()
}
func (s *Store) ListChats(ctx context.Context, conn string, limit, offset int) ([]Chat, error) {
	if e := s.active(ctx, conn); e != nil {
		return nil, e
	}
	limit, offset = page(limit, offset)
	rows, e := s.db.QueryContext(ctx, `SELECT body,total FROM (SELECT body,COUNT(*) OVER(PARTITION BY peer_key) AS total,ROW_NUMBER() OVER(PARTITION BY peer_key ORDER BY event_time DESC,id) AS rn,event_time FROM events WHERE connection_id=? AND peer_key<>':') WHERE rn=1 ORDER BY event_time DESC LIMIT ? OFFSET ?`, conn, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Chat{}
	for rows.Next() {
		var body string
		var chat Chat
		if e = rows.Scan(&body, &chat.Count); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(body), &chat.LastEvent); e != nil {
			return nil, e
		}
		chat.Peer = chat.LastEvent.Peer
		result = append(result, chat)
	}
	return result, rows.Err()
}

const deliveryColumns = `l.id,l.event_id,l.connection_id,d.alias,l.url,l.secret,l.device_config,l.revision,l.body,l.state,l.attempts,l.next_at,l.last_error,l.created_at,d.webhook_url,d.webhook_secret,d.webhook_revision`

// These partial indexes exclude retained delivery history. The predicates match
// the ordering subqueries exactly so SQLite can use them without ANALYZE.
var deliveryActiveIndexes = []string{
	`CREATE INDEX IF NOT EXISTS deliveries_pending_order ON deliveries(connection_id,url,queue_order) WHERE state IN ('queued','retry')`,
	`CREATE INDEX IF NOT EXISTS deliveries_inflight_target ON deliveries(connection_id,url) WHERE state='delivering'`,
}

const deliveryQueueOrderIndex = `CREATE INDEX deliveries_queue_order ON deliveries(queue_order)`

const deliveryClaimQuery = `SELECT ` + deliveryColumns + ` FROM deliveries l JOIN devices d ON d.connection_id=l.connection_id WHERE d.deleted_at IS NULL AND l.state IN ('queued','retry') AND l.next_at<=?
 AND NOT EXISTS(SELECT 1 FROM deliveries p WHERE p.connection_id=l.connection_id AND p.url=l.url AND p.state='delivering')
 AND NOT EXISTS(SELECT 1 FROM deliveries p WHERE p.connection_id=l.connection_id AND p.url=l.url AND p.state IN ('queued','retry') AND p.queue_order<l.queue_order)
 ORDER BY l.next_at,l.queue_order LIMIT ?`

func (s *Store) scanDelivery(row scanner) (v domains.Delivery, err error) {
	var ciphertext, currentSecret []byte
	var device bool
	var currentURL string
	var currentRevision int64
	var next, created int64
	var body string
	err = row.Scan(&v.ID, &v.EventID, &v.ConnectionID, &v.DeviceID, &v.URL, &ciphertext, &device, &v.Revision, &body, &v.State, &v.Attempts, &next, &v.LastError, &created, &currentURL, &currentSecret, &currentRevision)
	if err != nil {
		return v, dbError(err)
	}
	var p []byte
	if device && currentURL == v.URL && currentRevision == v.Revision {
		if len(currentSecret) > 0 {
			p, err = s.decrypt(currentSecret, v.ConnectionID+":webhook")
		}
	} else {
		p, err = s.decrypt(ciphertext, v.ID+":delivery")
	}
	if err != nil {
		return v, err
	}
	v.Device = device
	v.Secret = string(p)
	v.Body = json.RawMessage(body)
	v.NextAt = stamp(next)
	v.CreatedAt = stamp(created)
	return
}
func (s *Store) ClaimDeliveries(ctx context.Context, limit int, at time.Time) ([]domains.Delivery, error) {
	if limit <= 0 {
		limit = 8
	}
	if limit > 500 {
		limit = 500
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	// A URL change while an attempt was in flight must not resurrect its retries.
	if _, e = tx.ExecContext(ctx, `UPDATE deliveries SET state='paused',last_error='webhook URL changed; explicit replay required' WHERE state IN ('queued','retry') AND EXISTS(SELECT 1 FROM devices d WHERE d.connection_id=deliveries.connection_id AND (d.webhook_revision<>deliveries.revision OR (deliveries.device_config=1 AND d.webhook_url<>deliveries.url)))`); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, deliveryClaimQuery, at.UnixMilli(), limit)
	if e != nil {
		return nil, e
	}
	result := []domains.Delivery{}
	for rows.Next() {
		v, e := s.scanDelivery(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		result = append(result, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range result {
		if _, e = tx.ExecContext(ctx, `UPDATE deliveries SET state='delivering',attempts=attempts+1 WHERE id=? AND state IN ('queued','retry')`, result[i].ID); e != nil {
			return nil, e
		}
		result[i].State = "delivering"
		result[i].Attempts++
	}
	return result, tx.Commit()
}
func (s *Store) UpdateDelivery(ctx context.Context, conn, id, state string, next time.Time, lastError string) error {
	switch state {
	case "delivered", "retry", "failed", "paused", "cancelled":
	default:
		return domains.E("INVALID_DELIVERY_STATE", "invalid delivery state", 400)
	}
	// A manual retry cannot move a paused old-URL delivery; use ReplayDelivery.
	where := ` AND state='delivering'`
	if state == "retry" {
		where = ` AND state IN ('delivering','failed','retry')`
	}
	r, e := s.db.ExecContext(ctx, `UPDATE deliveries SET state=?,next_at=?,last_error=? WHERE connection_id=? AND id=?`+where+` AND EXISTS(SELECT 1 FROM devices WHERE connection_id=? AND deleted_at IS NULL)`, state, next.UnixMilli(), lastError, conn, id, conn)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return domains.E("DELIVERY_CONFLICT", "delivery missing or not eligible for this transition; paused deliveries require replay", 409)
	}
	return nil
}

// ReleaseDeliveryClaim is only for a claim that has not invoked HTTP yet. A
// transient lookup error or local shutdown must neither pause work nor consume
// a destination's attempt budget. Deletion/cancellation always wins the race.
func (s *Store) ReleaseDeliveryClaim(ctx context.Context, conn, id string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE deliveries SET state='retry',attempts=MAX(attempts-1,0),next_at=?,last_error='delivery deferred before HTTP request' WHERE connection_id=? AND id=? AND state='delivering' AND EXISTS(SELECT 1 FROM devices WHERE connection_id=? AND deleted_at IS NULL)`, now(), conn, id, conn)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domains.E("DELIVERY_CONFLICT", "delivery is no longer claimed", 409)
	}
	return nil
}
func (s *Store) GetDelivery(ctx context.Context, conn, id string) (domains.Delivery, error) {
	return s.scanDelivery(s.db.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM deliveries l JOIN devices d ON d.connection_id=l.connection_id WHERE l.connection_id=? AND l.id=? AND d.deleted_at IS NULL`, conn, id))
}
func (s *Store) ListDeliveries(ctx context.Context, conn string, limit, offset int) ([]domains.Delivery, error) {
	return s.ListDeliveriesFiltered(ctx, conn, limit, offset, "", true)
}

func (s *Store) ListDeliveriesFiltered(ctx context.Context, conn string, limit, offset int, state string, includePayload bool) ([]domains.Delivery, error) {
	if !validDeliveryState(state) {
		return nil, domains.E("INVALID_DELIVERY_STATE", "unknown delivery state", 400)
	}
	if e := s.active(ctx, conn); e != nil {
		return nil, e
	}
	limit, offset = page(limit, offset)
	columns := deliveryColumns
	if !includePayload {
		columns = strings.Replace(columns, "l.body,", "'null',", 1)
	}
	query := `SELECT ` + columns + ` FROM deliveries l JOIN devices d ON d.connection_id=l.connection_id WHERE l.connection_id=? AND d.deleted_at IS NULL`
	args := []any{conn}
	if state != "" {
		query += ` AND l.state=?`
		args = append(args, state)
	}
	query += ` ORDER BY l.created_at DESC,l.id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, e := s.db.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []domains.Delivery{}
	for rows.Next() {
		v, e := s.scanDelivery(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) ReplayDelivery(ctx context.Context, conn, id string, targets []WebhookTarget) ([]domains.Delivery, error) {
	if len(targets) == 0 {
		return nil, domains.E("WEBHOOK_NOT_CONFIGURED", "no webhook targets configured", 409)
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	device, e := s.scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn))
	if e != nil {
		return nil, e
	}
	var eventID, body string
	if e = tx.QueryRowContext(ctx, `SELECT event_id,body FROM deliveries WHERE connection_id=? AND id=?`, conn, id).Scan(&eventID, &body); e != nil {
		return nil, dbError(e)
	}
	result := []domains.Delivery{}
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target.URL] {
			continue
		}
		seen[target.URL] = true
		v, e := s.insertDelivery(ctx, tx, conn, eventID, body, target, device.Webhook, 0)
		if e != nil {
			return nil, e
		}
		v.DeviceID = device.ID
		result = append(result, v)
	}
	return result, tx.Commit()
}
func (s *Store) EventCount(ctx context.Context, conn string) (int, error) {
	if e := s.active(ctx, conn); e != nil {
		return 0, e
	}
	var count int
	e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE connection_id=?`, conn).Scan(&count)
	return count, e
}

// Marshal targets never emits secret values; useful only for diagnostics.
func (t WebhookTarget) String() string {
	return fmt.Sprintf("WebhookTarget{device:%t,revision:%d}", t.Device, t.Revision)
}

// RetryDelivery retries only this destination, retaining the original queue
// position and attempt ledger while cancelling the old attempt atomically.
// Explicit ReplayDelivery moves payloads to changed destinations or replays
// successful work with a new queue position.
func (s *Store) RetryDelivery(ctx context.Context, conn, id string) (domains.Delivery, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return domains.Delivery{}, e
	}
	defer tx.Rollback()
	d, e := s.scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn))
	if e != nil {
		return domains.Delivery{}, e
	}
	old, e := s.scanDelivery(tx.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM deliveries l JOIN devices d ON d.connection_id=l.connection_id WHERE l.connection_id=? AND l.id=?`, conn, id))
	if e != nil {
		return domains.Delivery{}, e
	}
	if old.State != "failed" && old.State != "retry" {
		return domains.Delivery{}, domains.E("DELIVERY_CONFLICT", "only failed or retry deliveries can be retried", 409)
	}
	if old.Revision != d.Webhook.Revision || (old.Device && old.URL != d.Webhook.URL) {
		return domains.Delivery{}, domains.E("DELIVERY_CONFLICT", "webhook target changed; explicit replay is required", 409)
	}
	var queueOrder int64
	if e = tx.QueryRowContext(ctx, `SELECT queue_order FROM deliveries WHERE connection_id=? AND id=?`, conn, id).Scan(&queueOrder); e != nil {
		return domains.Delivery{}, e
	}
	replacement, e := s.insertDelivery(ctx, tx, conn, old.EventID, string(old.Body), WebhookTarget{URL: old.URL, Secret: old.Secret, Revision: old.Revision, Device: old.Device}, d.Webhook, queueOrder)
	if e != nil {
		return domains.Delivery{}, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE deliveries SET state='cancelled' WHERE connection_id=? AND id=?`, conn, id); e != nil {
		return domains.Delivery{}, e
	}
	replacement.DeviceID = d.ID
	return replacement, tx.Commit()
}
