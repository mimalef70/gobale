package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/mimalef70/gobale/src/domains"
)

const operationColumns = `o.id,o.connection_id,d.alias,o.request,COALESCE(o.idempotency_key,''),o.payload_hash,o.state,o.result,o.error_code,o.error_message,o.created_at,o.updated_at,COALESCE((SELECT schedule_id FROM schedule_occurrences so WHERE so.operation_id=o.id AND so.connection_id=o.connection_id),''),(SELECT scheduled_for FROM schedule_occurrences so WHERE so.operation_id=o.id AND so.connection_id=o.connection_id)`

func scanOperation(row scanner) (op domains.Operation, e error) {
	var request string
	var result sql.NullString
	var created, updated int64
	var scheduled sql.NullInt64
	e = row.Scan(&op.ID, &op.ConnectionID, &op.DeviceID, &request, &op.IdempotencyKey, &op.PayloadHash, &op.State, &result, &op.ErrorCode, &op.ErrorMessage, &created, &updated, &op.ScheduleID, &scheduled)
	if e != nil {
		return op, dbError(e)
	}
	if e = json.Unmarshal([]byte(request), &op.Request); e != nil {
		return op, e
	}
	if result.Valid {
		if e = json.Unmarshal([]byte(result.String), &op.Result); e != nil {
			return op, e
		}
	}
	if scheduled.Valid {
		t := stamp(scheduled.Int64)
		op.ScheduledFor = &t
	}
	op.CreatedAt = stamp(created)
	op.UpdatedAt = stamp(updated)
	return
}
func requestHash(r domains.SendRequest) (string, error) {
	r.RequestID = ""
	p, e := json.Marshal(r)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:]), nil
}
func requestID() (string, error) {
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	v := binary.LittleEndian.Uint64(b[:]) & 0x7fffffffffffffff
	if v == 0 {
		v = 1
	}
	return strconv.FormatUint(v, 10), nil
}
func (s *Store) enqueueTx(ctx context.Context, tx *sql.Tx, conn string, req domains.SendRequest, key string, limit int) (domains.Operation, bool, error) {
	var empty domains.Operation
	if e := activeTx(ctx, tx, conn); e != nil {
		return empty, false, e
	}
	if len(key) > 256 {
		return empty, false, domains.E("INVALID_IDEMPOTENCY_KEY", "idempotency key exceeds 256 bytes", 400)
	}
	hash, e := requestHash(req)
	if e != nil {
		return empty, false, e
	}
	if key != "" {
		var reserved int
		e := tx.QueryRowContext(ctx, `SELECT 1 FROM schedule_idempotency WHERE connection_id=? AND idempotency_key=?`, conn, key).Scan(&reserved)
		if e == nil {
			return empty, false, domains.E("IDEMPOTENCY_CONFLICT", "idempotency key was already used for a schedule", 409)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return empty, false, e
		}
		op, e := scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations o JOIN devices d ON d.connection_id=o.connection_id WHERE o.connection_id=? AND o.idempotency_key=?`, conn, key))
		if e == nil {
			if op.PayloadHash != hash {
				return empty, false, domains.E("IDEMPOTENCY_CONFLICT", "idempotency key was already used for a different request", 409)
			}
			return op, false, nil
		}
		var de *domains.Error
		if !errors.As(e, &de) || de.Code != "NOT_FOUND" {
			return empty, false, e
		}
	}
	if e = validateMediaReferencesTx(ctx, tx, conn, req); e != nil {
		return empty, false, e
	}
	if limit <= 0 {
		limit = 10000
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE state IN ('queued','sending','unknown')`).Scan(&count); e != nil {
		return empty, false, e
	}
	if count >= limit {
		return empty, false, &domains.Error{Code: "QUEUE_FULL", Message: "outbound queue is full; retry later", HTTP: 429, Retryable: true}
	}
	if req.RequestID == "" {
		req.RequestID, e = requestID()
		if e != nil {
			return empty, false, e
		}
	}
	body, e := marshal(req)
	if e != nil {
		return empty, false, e
	}
	id := newID()
	created := now()
	var idem any
	if key != "" {
		idem = key
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO operations(id,connection_id,request,idempotency_key,payload_hash,state,created_at,updated_at,queue_order) VALUES(?,?,?,?,?,'queued',?,?,(SELECT COALESCE(MAX(queue_order),0)+1 FROM operations))`, id, conn, body, idem, hash, created, created); e != nil {
		return empty, false, e
	}
	op, e := scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations o JOIN devices d ON d.connection_id=o.connection_id WHERE o.id=?`, id))
	return op, true, e
}
func (s *Store) enqueue(ctx context.Context, conn string, req domains.SendRequest, key string, limit int) (domains.Operation, bool, error) {
	tx, e := s.beginTx(ctx)
	if e != nil {
		return domains.Operation{}, false, e
	}
	defer s.rollbackTx(tx)
	op, created, e := s.enqueueTx(ctx, tx, conn, req, key, limit)
	if e != nil {
		return op, created, e
	}
	return op, created, s.commitTx(tx)
}

// ClaimOperations picks at most one queued operation per device. A provider call
// is never made until the sending transition has committed.
func (s *Store) claimOperations(ctx context.Context, limit int) ([]domains.Operation, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	tx, e := s.beginTx(ctx)
	if e != nil {
		return nil, e
	}
	defer s.rollbackTx(tx)
	rows, e := tx.QueryContext(ctx, `SELECT `+operationColumns+` FROM operations o JOIN devices d ON d.connection_id=o.connection_id
 WHERE o.state='queued' AND d.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM operations p WHERE p.connection_id=o.connection_id AND p.state='sending')
 AND NOT EXISTS(SELECT 1 FROM operations p WHERE p.connection_id=o.connection_id AND p.state='queued' AND p.queue_order<o.queue_order)
 ORDER BY o.updated_at,o.queue_order LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	ops := []domains.Operation{}
	for rows.Next() {
		op, e := scanOperation(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		ops = append(ops, op)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	t := now()
	for i := range ops {
		if _, e = tx.ExecContext(ctx, `UPDATE operations SET state='sending',updated_at=? WHERE id=? AND state='queued'`, t, ops[i].ID); e != nil {
			return nil, e
		}
		ops[i].State = "sending"
		ops[i].UpdatedAt = stamp(t)
	}
	return ops, s.commitTx(tx)
}
func (s *Store) finishOperation(ctx context.Context, conn, id, state string, result *domains.SendResult, code, message string) error {
	switch state {
	case "queued", "succeeded", "failed", "unknown", "cancelled":
	default:
		return domains.E("INVALID_OPERATION_STATE", "invalid operation state", 400)
	}
	var body any
	if result != nil {
		p, e := marshal(result)
		if e != nil {
			return e
		}
		body = p
	}
	eligible := "sending"
	if state != "queued" {
		eligible = "unknown"
	}
	r, e := s.db.ExecContext(ctx, `UPDATE operations SET state=?,result=?,error_code=?,error_message=?,updated_at=? WHERE connection_id=? AND id=? AND state IN ('sending',?) AND EXISTS(SELECT 1 FROM devices WHERE connection_id=? AND deleted_at IS NULL)`, state, body, code, message, now(), conn, id, eligible, conn)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return domains.E("OPERATION_CONFLICT", "operation is missing or no longer in flight", 409)
	}
	return nil
}
func (s *Store) GetOperation(ctx context.Context, conn, id string) (domains.Operation, error) {
	return scanOperation(s.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations o JOIN devices d ON d.connection_id=o.connection_id WHERE o.connection_id=? AND o.id=? AND d.deleted_at IS NULL`, conn, id))
}
func (s *Store) ListOperations(ctx context.Context, conn string, limit, offset int) ([]domains.Operation, error) {
	if e := s.active(ctx, conn); e != nil {
		return nil, e
	}
	limit, offset = page(limit, offset)
	rows, e := s.db.QueryContext(ctx, `SELECT `+operationColumns+` FROM operations o JOIN devices d ON d.connection_id=o.connection_id WHERE o.connection_id=? ORDER BY o.created_at DESC,o.id LIMIT ? OFFSET ?`, conn, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ops := []domains.Operation{}
	for rows.Next() {
		op, e := scanOperation(rows)
		if e != nil {
			return nil, e
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}
