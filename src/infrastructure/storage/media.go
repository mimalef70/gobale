package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/mimalef70/goomni/src/domains"
)

func validateMediaReferencesTx(ctx context.Context, tx *sql.Tx, conn string, request domains.SendRequest) error {
	ids, err := domains.ReferencedMediaIDs(request)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM media WHERE connection_id=? AND id=?`, conn, id).Scan(&exists); err != nil {
			return dbError(err)
		}
	}
	return nil
}

// MediaFileRegistered is a conservative cleanup check, not an account API.
// Deleted connections and their retained uploads still protect files. A
// conflicting row or unknown connection is not evidence that a file is orphaned.
func (s *Store) MediaFileRegistered(ctx context.Context, conn, id, relative, absolute string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM devices WHERE connection_id=?`, conn).Scan(&exists); err != nil {
		return false, fmt.Errorf("media cleanup cannot verify connection")
	}
	// All production uploads use the canonical relative path or its legacy
	// absolute form. Check both independently of connection and media ID.
	rows, err := tx.QueryContext(ctx, `SELECT id,connection_id,path FROM media WHERE id=? OR path=? OR path=?`, id, relative, absolute)
	if err != nil {
		return false, err
	}
	registered := false
	for rows.Next() {
		var storedID, storedConn, path string
		if err = rows.Scan(&storedID, &storedConn, &path); err != nil {
			rows.Close()
			return false, err
		}
		if storedConn != conn || (id != "" && storedID != id) || (filepath.Clean(path) != filepath.Clean(relative) && filepath.Clean(path) != filepath.Clean(absolute)) {
			rows.Close()
			return false, fmt.Errorf("media cleanup found conflicting registration")
		}
		registered = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	return registered, tx.Commit()
}

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
	rows, e := tx.QueryContext(ctx, `SELECT request FROM operations WHERE connection_id=? AND state IN ('queued','sending','unknown') UNION ALL SELECT request FROM schedules WHERE connection_id=? AND state IN ('active','paused')`, conn, conn)
	if e != nil {
		return e
	}
	for rows.Next() {
		var body string
		if e = rows.Scan(&body); e != nil {
			rows.Close()
			return e
		}
		var request domains.SendRequest
		if e = json.Unmarshal([]byte(body), &request); e != nil {
			rows.Close()
			return errors.New("stored media references cannot be decoded")
		}
		ids, err := domains.ReferencedMediaIDs(request)
		if err != nil {
			rows.Close()
			return err
		}
		for _, referenced := range ids {
			if referenced == id {
				rows.Close()
				return domains.E("MEDIA_IN_USE", "media is referenced by pending work", 409)
			}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
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
