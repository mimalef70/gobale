package storage

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"time"
)

func workPredicates(alias, state, kind, operation, peer string, after, before *time.Time) (string, []any) {
	q := ""
	args := []any{}
	add := func(expr string, v any) { q += " AND " + expr; args = append(args, v) }
	if state != "" {
		add(alias+".state=?", state)
	}
	if kind != "" {
		add("COALESCE(NULLIF(json_extract("+alias+".request,'$.kind'),''),'text')=?", kind)
	}
	if operation != "" {
		add("json_extract("+alias+".request,'$.operation')=?", operation)
	}
	if peer != "" {
		add("(json_extract("+alias+".request,'$.peer.type')||':'||json_extract("+alias+".request,'$.peer.id'))=?", peer)
	}
	if after != nil {
		add(alias+".created_at>=?", filterMillis(*after))
	}
	if before != nil {
		add(alias+".created_at<?", filterMillis(*before))
	}
	return q, args
}
func (s *Store) ListOperationsFiltered(ctx context.Context, conn string, f domains.OperationFilter) ([]domains.Operation, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if err := s.active(ctx, conn); err != nil {
		return nil, err
	}
	if f.ScheduleID != "" {
		if _, err := s.GetSchedule(ctx, conn, f.ScheduleID); err != nil {
			return nil, err
		}
	}
	q := `SELECT ` + operationColumns + ` FROM operations o JOIN devices d ON d.connection_id=o.connection_id WHERE o.connection_id=?`
	args := []any{conn}
	pred, more := workPredicates("o", f.State, f.Kind, f.Operation, f.Peer, f.CreatedAfter, f.CreatedBefore)
	q += pred
	args = append(args, more...)
	if f.ScheduleID != "" {
		q += ` AND EXISTS(SELECT 1 FROM schedule_occurrences so WHERE so.connection_id=o.connection_id AND so.operation_id=o.id AND so.schedule_id=?)`
		args = append(args, f.ScheduleID)
	}
	limit, offset := page(f.Limit, f.Offset)
	q += ` ORDER BY o.created_at DESC,o.id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domains.Operation{}
	for rows.Next() {
		v, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) ListSchedulesFiltered(ctx context.Context, conn string, f domains.ScheduleFilter) ([]domains.Schedule, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if err := s.active(ctx, conn); err != nil {
		return nil, err
	}
	q := `SELECT ` + scheduleColumns + ` FROM schedules s JOIN devices d ON d.connection_id=s.connection_id WHERE s.connection_id=?`
	args := []any{conn}
	pred, more := workPredicates("s", f.State, f.Kind, f.Operation, f.Peer, f.CreatedAfter, f.CreatedBefore)
	q += pred
	args = append(args, more...)
	limit, offset := page(f.Limit, f.Offset)
	q += ` ORDER BY s.created_at DESC,s.id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domains.Schedule{}
	for rows.Next() {
		v, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) ListScheduleOccurrences(ctx context.Context, conn, id, state string, limit, offset int) ([]domains.ScheduleOccurrence, error) {
	if !domains.ValidOperationState(state) {
		return nil, domains.E("INVALID_FILTER", "invalid operation state", 400)
	}
	if _, err := s.GetSchedule(ctx, conn, id); err != nil {
		return nil, err
	}
	q := `SELECT so.occurrence_number,so.scheduled_for,` + operationColumns + ` FROM schedule_occurrences so JOIN operations o ON o.connection_id=so.connection_id AND o.id=so.operation_id JOIN devices d ON d.connection_id=o.connection_id WHERE so.connection_id=? AND so.schedule_id=?`
	args := []any{conn, id}
	if state != "" {
		q += ` AND o.state=?`
		args = append(args, state)
	}
	limit, offset = page(limit, offset)
	q += ` ORDER BY so.scheduled_for DESC,o.id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domains.ScheduleOccurrence{}
	for rows.Next() {
		v := domains.ScheduleOccurrence{ScheduleID: id}
		var scheduled int64
		op, err := scanOperation(&prefixScanner{scanner: rows, prefix: []any{&v.Number, &scheduled}})
		if err != nil {
			return nil, err
		}
		v.ScheduledFor = stamp(scheduled)
		v.Operation = op
		result = append(result, v)
	}
	return result, rows.Err()
}

type prefixScanner struct {
	scanner
	prefix []any
}

func (r *prefixScanner) Scan(dest ...any) error { return r.scanner.Scan(append(r.prefix, dest...)...) }

// Search only the event's own reviewed text or document caption. Quoted replies,
// IDs, media credentials and arbitrary JSON fields are deliberately excluded.
const eventSearchText = `CASE json_extract(body,'$.payload.kind') WHEN 'text' THEN COALESCE(json_extract(body,'$.payload.message'),'') WHEN 'document' THEN COALESCE(json_extract(body,'$.payload.caption'),'') ELSE '' END`
const eventDirection = `CASE WHEN type IN ('message','message.edited') AND (json_type(body,'$.sender_id') IS NOT 'text' OR CAST(json_extract(body,'$.sender_id') AS INTEGER) NOT BETWEEN 1 AND 4294967295 OR CAST(CAST(json_extract(body,'$.sender_id') AS INTEGER) AS TEXT)<>json_extract(body,'$.sender_id')) THEN 'unknown' WHEN json_extract(body,'$.direction') IN ('incoming','outgoing') THEN json_extract(body,'$.direction') ELSE 'unknown' END`

func (s *Store) ListEventsFiltered(ctx context.Context, conn string, f domains.EventFilter) ([]domains.Event, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if err := s.active(ctx, conn); err != nil {
		return nil, err
	}
	q := `SELECT body FROM events WHERE connection_id=?`
	args := []any{conn}
	add := func(expr string, v any) { q += " AND " + expr; args = append(args, v) }
	if f.Peer != "" {
		add("peer_key=?", f.Peer)
	}
	if f.Search != "" {
		add("instr(("+eventSearchText+"),?)>0", f.Search)
	}
	if f.Event != "" {
		add("type=?", f.Event)
	}
	if f.Direction != "" {
		add("("+eventDirection+")=?", f.Direction)
	}
	if f.SenderID != "" {
		add("json_extract(body,'$.sender_id')=?", f.SenderID)
	}
	if f.StartTime != nil {
		add("event_time>=?", filterMillis(*f.StartTime))
	}
	if f.EndTime != nil {
		add("event_time<?", filterMillis(*f.EndTime))
	}
	if f.MediaOnly {
		q += ` AND json_extract(body,'$.payload.kind')='document'`
	}
	limit, offset := page(f.Limit, f.Offset)
	q += ` ORDER BY event_time DESC,id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domains.Event{}
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		var v domains.Event
		if err = json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// Millisecond storage compares against the ceiling of submillisecond bounds.
func filterMillis(t time.Time) int64 {
	n := t.UnixMilli()
	if t.Nanosecond()%int(time.Millisecond) != 0 {
		n++
	}
	return n
}
