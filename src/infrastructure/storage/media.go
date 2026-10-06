package storage

import (
	"context"
	"github.com/mimalef70/gobale/src/domains"
)

func (s *Store) SaveMedia(ctx context.Context, v domains.Media) error {
	if v.ID == "" || v.Path == "" || v.Size < 0 {
		return domains.E("INVALID_MEDIA", "media id, path and non-negative size are required", 400)
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = stamp(now())
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = activeTx(ctx, tx, v.ConnectionID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO media(id,connection_id,name,content_type,size,path,created_at) VALUES(?,?,?,?,?,?,?)`, v.ID, v.ConnectionID, v.Name, v.ContentType, v.Size, v.Path, v.CreatedAt.UnixMilli()); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) GetMedia(ctx context.Context, conn, id string) (domains.Media, error) {
	var v domains.Media
	var created int64
	e := s.db.QueryRowContext(ctx, `SELECT m.id,m.connection_id,m.name,m.content_type,m.size,m.path,m.created_at FROM media m JOIN devices d ON d.connection_id=m.connection_id WHERE m.connection_id=? AND m.id=? AND d.deleted_at IS NULL`, conn, id).Scan(&v.ID, &v.ConnectionID, &v.Name, &v.ContentType, &v.Size, &v.Path, &created)
	v.CreatedAt = stamp(created)
	return v, dbError(e)
}
func (s *Store) DeleteMedia(ctx context.Context, conn, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e := activeTx(ctx, tx, conn); e != nil {
		return e
	}
	// Outstanding sends and active/paused schedules pin their media until done.
	var used int
	e = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM operations WHERE connection_id=? AND state IN ('queued','sending','unknown') AND json_extract(request,'$.media_id')=?)+(SELECT COUNT(*) FROM schedules WHERE connection_id=? AND state IN ('active','paused') AND json_extract(request,'$.media_id')=?)`, conn, id, conn, id).Scan(&used)
	if e != nil {
		return e
	}
	if used > 0 {
		return domains.E("MEDIA_IN_USE", "media is referenced by pending work", 409)
	}
	r, e := tx.ExecContext(ctx, `DELETE FROM media WHERE connection_id=? AND id=?`, conn, id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return notFound()
	}
	return tx.Commit()
}
