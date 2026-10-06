package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Backup creates a transactionally consistent SQLite snapshot without replacing
// any existing file. It contains encrypted sessions, but not the external key or
// media files. A same-directory temporary file is synced before its final name
// appears atomically; on failure the incomplete snapshot is removed.
func (s *Store) Backup(ctx context.Context, destination string) (err error) {
	if destination == "" {
		return fmt.Errorf("backup destination is required")
	}
	dst, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(dst); err == nil {
		return fmt.Errorf("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The caller chooses an existing directory; do not create a directory tree
	// unexpectedly when a backup path contains a typo.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".gobale-backup-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Close(); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, `VACUUM INTO ?`, name); err != nil {
		return fmt.Errorf("create SQLite snapshot: %w", err)
	}
	completed, err := os.OpenFile(name, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = completed.Sync()
	closeErr := completed.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(name, dst); err != nil {
		return fmt.Errorf("publish backup without replacing existing files: %w", err)
	}
	dir, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
