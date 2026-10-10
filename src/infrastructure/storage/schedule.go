package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/domains/send"
)

const scheduleColumns = `s.id,s.connection_id,d.alias,d.provider,s.request,s.state,s.next_at,s.occurrence_count,s.created_at,(s.occurrence_count=(SELECT COUNT(*) FROM schedule_occurrences so WHERE so.connection_id=s.connection_id AND so.schedule_id=s.id))`
const scheduleIdempotencySchema = `CREATE TABLE schedule_idempotency(connection_id TEXT NOT NULL REFERENCES devices(connection_id),idempotency_key TEXT NOT NULL,payload_hash TEXT NOT NULL,schedule_id TEXT NOT NULL UNIQUE REFERENCES schedules(id),PRIMARY KEY(connection_id,idempotency_key))`

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func lookupScheduleIdempotent(ctx context.Context, q rowQuerier, conn string, request domains.SendRequest, key string) (domains.Schedule, bool, error) {
	wanted, err := requestHash(request)
	if err != nil {
		return domains.Schedule{}, false, err
	}
	return lookupScheduleHash(ctx, q, conn, wanted, key)
}

func lookupScheduleHash(ctx context.Context, q rowQuerier, conn, wanted, key string) (domains.Schedule, bool, error) {
	if len(key) > 256 {
		return domains.Schedule{}, false, domains.E("INVALID_IDEMPOTENCY_KEY", "idempotency key exceeds 256 bytes", 400)
	}
	if key == "" {
		return domains.Schedule{}, false, nil
	}
	var existing int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE connection_id=? AND idempotency_key=?`, conn, key).Scan(&existing)
	if err == nil {
		return domains.Schedule{}, false, domains.E("IDEMPOTENCY_CONFLICT", "idempotency key was already used for an immediate operation", 409)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domains.Schedule{}, false, err
	}
	var hash, id string
	err = q.QueryRowContext(ctx, `SELECT payload_hash,schedule_id FROM schedule_idempotency WHERE connection_id=? AND idempotency_key=?`, conn, key).Scan(&hash, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return domains.Schedule{}, false, nil
	}
	if err != nil {
		return domains.Schedule{}, false, err
	}
	if wanted != hash {
		return domains.Schedule{}, false, domains.E("IDEMPOTENCY_CONFLICT", "idempotency key was already used for a different schedule request", 409)
	}
	schedule, err := scanSchedule(q.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules s JOIN devices d ON d.connection_id=s.connection_id WHERE s.connection_id=? AND s.id=? AND d.deleted_at IS NULL`, conn, id))
	return schedule, err == nil, err
}

// LookupScheduleIdempotent lets callers recognize an already-created schedule
// before revalidating its now-past start time or a subsequently-deleted media file.
func (s *Store) LookupScheduleIdempotent(ctx context.Context, conn string, request domains.SendRequest, key string) (domains.Schedule, bool, error) {
	if err := s.active(ctx, conn); err != nil {
		return domains.Schedule{}, false, err
	}
	return lookupScheduleIdempotent(ctx, s.db, conn, request, key)
}

func scanSchedule(row scanner) (v domains.Schedule, e error) {
	var request string
	var next, created int64
	e = row.Scan(&v.ID, &v.ConnectionID, &v.DeviceID, &v.Provider, &request, &v.State, &next, &v.Count, &created, &v.OccurrenceHistoryComplete)
	if e != nil {
		return v, dbError(e)
	}
	e = json.Unmarshal([]byte(request), &v.Request)
	v.NextAt = stamp(next)
	v.CreatedAt = stamp(created)
	return
}
func (s *Store) CreateSchedule(ctx context.Context, conn string, request domains.SendRequest, next time.Time) (domains.Schedule, error) {
	return s.CreateScheduleIdempotent(ctx, conn, request, next, "")
}

func (s *Store) createScheduleIdempotent(ctx context.Context, conn string, request domains.SendRequest, next time.Time, key string) (domains.Schedule, error) {
	hash, e := requestHash(request)
	if e != nil {
		return domains.Schedule{}, e
	}
	tx, e := s.beginTx(ctx)
	if e != nil {
		return domains.Schedule{}, e
	}
	defer s.rollbackTx(tx)
	v, e := s.createScheduleTx(ctx, tx, conn, request, next, key, hash)
	if e != nil {
		return v, e
	}
	return v, s.commitTx(tx)
}

func (s *Store) createScheduleTx(ctx context.Context, tx *sql.Tx, conn string, request domains.SendRequest, next time.Time, key, hash string) (domains.Schedule, error) {
	body, e := marshal(request)
	if e != nil {
		return domains.Schedule{}, e
	}
	if e = activeTx(ctx, tx, conn); e != nil {
		return domains.Schedule{}, e
	}
	if existing, found, e := lookupScheduleHash(ctx, tx, conn, hash, key); e != nil {
		return domains.Schedule{}, e
	} else if found {
		return existing, nil
	}
	if next.IsZero() {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", "next run time is required", 400)
	}
	if e = validateMediaReferencesTx(ctx, tx, conn, request); e != nil {
		return domains.Schedule{}, e
	}
	id := newID()
	if _, e = tx.ExecContext(ctx, `INSERT INTO schedules(id,connection_id,request,state,next_at,created_at) VALUES(?,?,?,'active',?,?)`, id, conn, body, next.UnixMilli(), now()); e != nil {
		return domains.Schedule{}, e
	}
	if key != "" {
		if _, e = tx.ExecContext(ctx, `INSERT INTO schedule_idempotency(connection_id,idempotency_key,payload_hash,schedule_id) VALUES(?,?,?,?)`, conn, key, hash, id); e != nil {
			return domains.Schedule{}, e
		}
	}
	v, e := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules s JOIN devices d ON d.connection_id=s.connection_id WHERE s.id=?`, id))
	if e != nil {
		return v, e
	}
	return v, nil
}
func (s *Store) GetSchedule(ctx context.Context, conn, id string) (domains.Schedule, error) {
	return scanSchedule(s.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules s JOIN devices d ON d.connection_id=s.connection_id WHERE s.connection_id=? AND s.id=? AND d.deleted_at IS NULL`, conn, id))
}
func (s *Store) ListSchedules(ctx context.Context, conn string, limit, offset int) ([]domains.Schedule, error) {
	if e := s.active(ctx, conn); e != nil {
		return nil, e
	}
	limit, offset = page(limit, offset)
	rows, e := s.db.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules s JOIN devices d ON d.connection_id=s.connection_id WHERE s.connection_id=? ORDER BY s.created_at DESC,s.id LIMIT ? OFFSET ?`, conn, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []domains.Schedule{}
	for rows.Next() {
		v, e := scanSchedule(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) SetScheduleState(ctx context.Context, conn, id, state string) error {
	switch state {
	case "active", "paused", "cancelled", "completed", "failed":
	default:
		return domains.E("INVALID_SCHEDULE_STATE", "invalid schedule state", 400)
	}
	r, e := s.db.ExecContext(ctx, `UPDATE schedules SET state=? WHERE connection_id=? AND id=? AND state IN ('active','paused') AND EXISTS(SELECT 1 FROM devices WHERE connection_id=? AND deleted_at IS NULL)`, state, conn, id, conn)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return domains.E("SCHEDULE_CONFLICT", "schedule is missing, cancelled or completed", 409)
	}
	return nil
}

// DueSchedules pages internal scheduler work by its stable due-time/id order.
// Empty afterID starts from the beginning. The caller rotates across pages even
// when admission fails, so a blocked prefix cannot hide another connection.
func (s *Store) DueSchedules(ctx context.Context, at time.Time, limit int, afterNextAt time.Time, afterID string) ([]domains.Schedule, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	query := `SELECT ` + scheduleColumns + ` FROM schedules s JOIN devices d ON d.connection_id=s.connection_id WHERE s.state='active' AND s.next_at<=? AND d.deleted_at IS NULL`
	args := []any{at.UnixMilli()}
	if afterID != "" {
		query += ` AND (s.next_at>? OR (s.next_at=? AND s.id>?))`
		args = append(args, afterNextAt.UnixMilli(), afterNextAt.UnixMilli(), afterID)
	}
	query += ` ORDER BY s.next_at,s.id LIMIT ?`
	args = append(args, limit)
	rows, e := s.db.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []domains.Schedule{}
	for rows.Next() {
		v, e := scanSchedule(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// MaterializeSchedule atomically consumes the exact due occurrence, enqueues its
// send and advances the schedule. Passing nil next completes a one-shot or final
// recurrence. The runtime computes the next time with the shared calendar rules.
func (s *Store) materializeSchedule(ctx context.Context, conn, id string, expected time.Time, next *time.Time, limits AdmissionLimits) (domains.Operation, error) {
	tx, e := s.beginTx(ctx)
	if e != nil {
		return domains.Operation{}, e
	}
	defer s.rollbackTx(tx)
	v, e := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules s JOIN devices d ON d.connection_id=s.connection_id WHERE s.connection_id=? AND s.id=? AND d.deleted_at IS NULL`, conn, id))
	if e != nil {
		return domains.Operation{}, e
	}
	if v.State != "active" || v.NextAt.UnixMilli() != expected.UnixMilli() {
		return domains.Operation{}, domains.E("SCHEDULE_CONFLICT", "schedule occurrence was already consumed or changed", 409)
	}
	if next != nil && next.UnixMilli() <= expected.UnixMilli() {
		return domains.Operation{}, domains.E("INVALID_SCHEDULE", "next occurrence must be after the consumed occurrence", 400)
	}
	request := v.Request
	request.ScheduleOptions = send.ScheduleOptions{}
	request.RequestID = ""
	op, _, e := s.enqueueTx(ctx, tx, conn, request, "", limits)
	if e != nil {
		return op, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO schedule_occurrences(connection_id,schedule_id,occurrence_number,scheduled_for,operation_id,created_at) VALUES(?,?,?,?,?,?)`, conn, id, v.Count+1, expected.UnixMilli(), op.ID, now()); e != nil {
		return op, e
	}
	op.ScheduleID = id
	t := stamp(expected.UnixMilli())
	op.ScheduledFor = &t
	state := "completed"
	nextMS := expected.UnixMilli()
	if next != nil {
		state = "active"
		nextMS = next.UnixMilli()
	}
	if _, e = tx.ExecContext(ctx, `UPDATE schedules SET state=?,next_at=?,occurrence_count=occurrence_count+1 WHERE id=? AND connection_id=?`, state, nextMS, id, conn); e != nil {
		return op, e
	}
	return op, s.commitTx(tx)
}
