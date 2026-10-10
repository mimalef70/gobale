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
	"math"
	"strconv"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

const operationColumns = `o.id,o.connection_id,d.alias,d.provider,o.request,COALESCE(o.idempotency_key,''),o.payload_hash,o.state,o.result,o.error_code,o.error_message,o.created_at,o.updated_at,COALESCE((SELECT schedule_id FROM schedule_occurrences so WHERE so.operation_id=o.id AND so.connection_id=o.connection_id),''),(SELECT scheduled_for FROM schedule_occurrences so WHERE so.operation_id=o.id AND so.connection_id=o.connection_id)`

func scanOperation(row scanner) (op domains.Operation, e error) {
	var request string
	var result sql.NullString
	var created, updated int64
	var scheduled sql.NullInt64
	e = row.Scan(&op.ID, &op.ConnectionID, &op.DeviceID, &op.Provider, &request, &op.IdempotencyKey, &op.PayloadHash, &op.State, &result, &op.ErrorCode, &op.ErrorMessage, &created, &updated, &op.ScheduleID, &scheduled)
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
func (s *Store) enqueueTx(ctx context.Context, tx *sql.Tx, conn string, req domains.SendRequest, key string, limits AdmissionLimits) (domains.Operation, bool, error) {
	hash, err := requestHash(req)
	if err != nil {
		return domains.Operation{}, false, err
	}
	return s.enqueueHashedTx(ctx, tx, conn, req, key, limits, hash)
}

func (s *Store) enqueueHashedTx(ctx context.Context, tx *sql.Tx, conn string, req domains.SendRequest, key string, limits AdmissionLimits, hash string) (domains.Operation, bool, error) {
	var empty domains.Operation
	if e := activeTx(ctx, tx, conn); e != nil {
		return empty, false, e
	}
	if len(key) > 256 {
		return empty, false, domains.E("INVALID_IDEMPOTENCY_KEY", "idempotency key exceeds 256 bytes", 400)
	}
	var e error
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
	limits = limits.normalized()
	var count, connectionCount int
	// Filter by indexed outstanding states, rather than a connection's retained
	// terminal history. Check both counts atomically with insertion, after idempotency.
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(connection_id=?),0) FROM operations WHERE state IN ('queued','sending','unknown')`, conn).Scan(&count, &connectionCount); e != nil {
		return empty, false, e
	}
	if count >= limits.Global {
		return empty, false, &domains.Error{Code: "QUEUE_FULL", Message: "outbound queue is full; retry later", HTTP: 429, Retryable: true}
	}
	if connectionCount >= limits.Connection {
		return empty, false, &domains.Error{Code: "CONNECTION_QUEUE_FULL", Message: "connection outbound queue is full; retry later", HTTP: 429, Retryable: true}
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
func (s *Store) enqueue(ctx context.Context, conn string, req domains.SendRequest, key string, limits AdmissionLimits) (domains.Operation, bool, error) {
	tx, e := s.beginTx(ctx)
	if e != nil {
		return domains.Operation{}, false, e
	}
	defer s.rollbackTx(tx)
	op, created, e := s.enqueueTx(ctx, tx, conn, req, key, limits)
	if e != nil {
		return op, created, e
	}
	return op, created, s.commitTx(tx)
}

// SetSendConcurrency configures admission to provider calls. Configure before
// starting workers. Each provider with active connections gets a bounded share;
// a disconnected provider cannot occupy every worker in a mixed installation.
func (s *Store) SetSendConcurrency(workers int) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if workers < 1 {
		workers = 1
	}
	s.sendConcurrency = workers
}

// ClaimOperations persists both the sending transition and the dispatch turn
// before a provider call. Fair turns survive restart and retained history does
// not participate in selecting the next connection.
func (s *Store) claimOperations(ctx context.Context, limit int) ([]domains.Operation, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	tx, e := s.beginTx(ctx)
	if e != nil {
		return nil, e
	}
	defer s.rollbackTx(tx)
	active := map[domains.Provider]int{}
	total := 0
	rows, e := tx.QueryContext(ctx, `SELECT d.provider,COUNT(*) FROM operations o JOIN devices d ON d.connection_id=o.connection_id WHERE o.state='sending' AND d.deleted_at IS NULL GROUP BY d.provider`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var provider domains.Provider
		var count int
		if e = rows.Scan(&provider, &count); e != nil {
			rows.Close()
			return nil, e
		}
		active[provider] = count
		total += count
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	budget := 500
	if s.sendConcurrency > 0 {
		var providers int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT provider) FROM devices WHERE deleted_at IS NULL`).Scan(&providers); e != nil {
			return nil, e
		}
		budget = max(1, s.sendConcurrency/max(1, providers))
	}
	var turn int64
	if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_claim),0) FROM connection_dispatch`).Scan(&turn); e != nil {
		return nil, e
	}
	ops := []domains.Operation{}
	for len(ops) < limit && (s.sendConcurrency == 0 || total < s.sendConcurrency) {
		allowed := []any{}
		for _, provider := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
			if active[provider] < budget {
				allowed = append(allowed, provider)
			}
		}
		if len(allowed) == 0 {
			break
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(allowed)), ",")
		query := `SELECT ` + operationColumns + ` FROM operations o JOIN devices d ON d.connection_id=o.connection_id
 LEFT JOIN connection_dispatch cd ON cd.connection_id=o.connection_id
 LEFT JOIN (SELECT owner.provider,MAX(turns.last_claim) AS last_claim FROM connection_dispatch turns JOIN devices owner ON owner.connection_id=turns.connection_id GROUP BY owner.provider) pd ON pd.provider=d.provider
 WHERE o.state='queued' AND d.deleted_at IS NULL AND d.provider IN (` + placeholders + `)
 AND NOT EXISTS(SELECT 1 FROM operations p WHERE p.connection_id=o.connection_id AND p.state='sending')
 AND NOT EXISTS(SELECT 1 FROM operations p WHERE p.connection_id=o.connection_id AND p.state='queued' AND p.queue_order<o.queue_order)
 ORDER BY (SELECT COUNT(*) FROM operations running JOIN devices owner ON owner.connection_id=running.connection_id WHERE running.state='sending' AND owner.provider=d.provider),COALESCE(pd.last_claim,0),COALESCE(cd.last_claim,0),o.updated_at,o.queue_order LIMIT 1`
		op, err := scanOperation(tx.QueryRowContext(ctx, query, allowed...))
		if err != nil {
			var de *domains.Error
			if errors.As(err, &de) && de.Code == "NOT_FOUND" {
				break
			}
			return nil, err
		}
		t := now()
		if _, e = tx.ExecContext(ctx, `UPDATE operations SET state='sending',updated_at=? WHERE id=? AND state='queued'`, t, op.ID); e != nil {
			return nil, e
		}
		if turn == math.MaxInt64 {
			return nil, domains.E("DISPATCH_LIMIT", "dispatch sequence exhausted", 500)
		}
		turn++
		if _, e = tx.ExecContext(ctx, `INSERT INTO connection_dispatch(connection_id,last_claim) VALUES(?,?) ON CONFLICT(connection_id) DO UPDATE SET last_claim=excluded.last_claim`, op.ConnectionID, turn); e != nil {
			return nil, e
		}
		op.State = "sending"
		op.UpdatedAt = stamp(t)
		ops = append(ops, op)
		active[op.Provider]++
		total++
	}
	if e = s.commitTx(tx); e != nil {
		return nil, e
	}
	return ops, nil
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
