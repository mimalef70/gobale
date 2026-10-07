package mediafile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// Lookup must include tombstoned connections and must fail closed on database
// errors or conflicting registrations. Paths are derived by Manager, never by
// the upload caller. True protects an already registered shared upload.
type Lookup func(context.Context, string, string, string, string) (bool, error)

const stagingName = ".gobale-staging"

type intent struct {
	ConnectionID string `json:"connection_id"`
	MediaID      string `json:"media_id"`
}

// Manager owns only private staging intents. It never expires registered media
// or infers ownership of an unregistered final file from its UUID-shaped name.
type Manager struct {
	root   string
	lookup Lookup
	lock   *os.File
	mu     sync.Mutex
	active map[string]bool
	closed bool
	scanMu sync.Mutex
	scan   *os.File
	// Diagnostic receives a fixed content-free code, never paths or payloads.
	Diagnostic func(string)
}

func Open(root string, lookup Lookup) (*Manager, error) {
	if root == "" || lookup == nil {
		return nil, errors.New("media root and registration lookup are required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("media root cannot be created")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, errors.New("media root cannot be resolved")
	}
	fd, err := unix.Open(filepath.Join(abs, ".gobale-media.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("media root ownership cannot be established")
	}
	lock := os.NewFile(uintptr(fd), "media-root-lock")
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		lock.Close()
		return nil, errors.New("invalid media root lock")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("media root is already owned by another process")
	}
	m := &Manager{root: abs, lookup: lookup, lock: lock, active: make(map[string]bool)}
	if err = ensureDirectory(filepath.Join(abs, stagingName)); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

func validID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

func ensureDirectory(path string) error {
	err := os.Mkdir(path, 0700)
	created := err == nil
	if err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("media directory cannot be created")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe media directory")
	}
	if created {
		return syncDirectory(filepath.Dir(path))
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

type Upload struct {
	File *os.File
	m    *Manager
	v    intent
	dir  string
	once sync.Once
}

func (u *Upload) ID() string { return u.v.MediaID }
func (u *Upload) RelativePath() string {
	return filepath.ToSlash(filepath.Join(u.v.ConnectionID, u.v.MediaID))
}

// Begin durably records ownership before any message bytes or final file exist.
func (m *Manager) Begin(ctx context.Context, conn string) (*Upload, error) {
	if !validID(conn) {
		return nil, errors.New("invalid media connection")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("media manager is closed")
	}
	m.active[id] = true
	m.mu.Unlock()
	u := &Upload{m: m, v: intent{ConnectionID: conn, MediaID: id}, dir: filepath.Join(m.root, stagingName, id)}
	if err := m.begin(u); err != nil {
		u.Release()
		return nil, err
	}
	return u, nil
}

func (m *Manager) begin(u *Upload) error {
	if err := ensureDirectory(filepath.Join(m.root, stagingName)); err != nil {
		return err
	}
	if err := os.Mkdir(u.dir, 0700); err != nil {
		return errors.New("media intent cannot be created")
	}
	body, _ := json.Marshal(u.v)
	f, err := os.OpenFile(filepath.Join(u.dir, "intent.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = syncDirectory(u.dir); err != nil {
		return err
	}
	if err = syncDirectory(filepath.Dir(u.dir)); err != nil {
		return err
	}
	u.File, err = os.OpenFile(filepath.Join(u.dir, "data"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	return err
}

// Publish makes the complete file durable before its database registration.
// The intent remains until Release verifies that registration independently.
func (u *Upload) Publish() error {
	if err := u.File.Sync(); err != nil {
		return err
	}
	if err := u.File.Close(); err != nil {
		return err
	}
	dir := filepath.Join(u.m.root, u.v.ConnectionID)
	if err := ensureDirectory(dir); err != nil {
		return err
	}
	// Link publishes without replacing any existing file, including a symlink.
	if err := os.Link(filepath.Join(u.dir, "data"), filepath.Join(dir, u.v.MediaID)); err != nil {
		return errors.New("media file cannot be published")
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	// Retain the staging hard link until cleanup. It proves that an unregistered
	// final file is ours even after a crash, and protects an unexpected collision
	// when publication failed without replacing the existing destination.
	return nil
}

// Release is safe even when Commit returned an ambiguous error: cleanup first
// re-reads registration, and a database failure leaves the intent for retry.
func (u *Upload) Release() {
	u.once.Do(func() {
		if u.File != nil {
			_ = u.File.Close()
		}
		u.m.mu.Lock()
		delete(u.m.active, u.v.MediaID)
		u.m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		u.m.scanMu.Lock()
		err := u.m.cleanup(ctx, u.v.MediaID)
		u.m.scanMu.Unlock()
		if err != nil {
			u.m.diagnostic()
		}
	})
}

func (m *Manager) diagnostic() {
	if m.Diagnostic != nil {
		m.Diagnostic("MEDIA_CLEANUP_DEFERRED")
	}
}

func regularOrAbsent(path string) (bool, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !st.Mode().IsRegular() {
		return false, errors.New("unsafe media cleanup file")
	}
	return true, nil
}

func (m *Manager) cleanup(ctx context.Context, id string) error {
	if !validID(id) {
		return errors.New("invalid media intent directory")
	}
	m.mu.Lock()
	active, closed := m.active[id], m.closed
	m.mu.Unlock()
	if active || closed {
		return nil
	}
	if err := ensureDirectory(filepath.Join(m.root, stagingName)); err != nil {
		return err
	}
	dir := filepath.Join(m.root, stagingName, id)
	st, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe media intent directory")
	}
	if ok, err := regularOrAbsent(filepath.Join(dir, "intent.json")); err != nil || !ok {
		if err != nil {
			return err
		}
		// A crash before writing the intent may leave an empty directory only.
		return os.Remove(dir)
	}
	f, err := os.Open(filepath.Join(dir, "intent.json"))
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(f, 1025))
	f.Close()
	if err != nil || len(body) > 1024 {
		return errors.New("invalid media cleanup intent")
	}
	var v intent
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&v); err != nil || !validID(v.ConnectionID) || v.MediaID != id {
		return errors.New("invalid media cleanup intent")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid media cleanup intent")
	}
	listing, err := os.Open(dir)
	if err != nil {
		return err
	}
	entries, readErr := listing.ReadDir(3)
	listing.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	for _, entry := range entries {
		if entry.Name() != "intent.json" && entry.Name() != "data" {
			return errors.New("unexpected media staging entries")
		}
	}
	dataPath := filepath.Join(dir, "data")
	dataExists, err := regularOrAbsent(dataPath)
	if err != nil {
		return err
	}
	rel := filepath.ToSlash(filepath.Join(v.ConnectionID, v.MediaID))
	final := filepath.Join(m.root, filepath.FromSlash(rel))
	// Absence of the connection directory is fine before publication; any
	// symlink or unexpected object at that boundary must be preserved.
	parent, err := os.Lstat(filepath.Dir(final))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (!parent.IsDir() || parent.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe media connection directory")
	}
	registered, err := m.lookup(ctx, v.ConnectionID, v.MediaID, rel, final)
	if err != nil {
		return errors.New("media registration cannot be verified")
	}
	finalExists, err := regularOrAbsent(final)
	if err != nil {
		return err
	}
	if registered && !finalExists {
		return errors.New("registered media file is unavailable")
	}
	if !registered {
		if finalExists {
			dataInfo, dataErr := os.Lstat(dataPath)
			finalInfo, finalErr := os.Lstat(final)
			if !dataExists || dataErr != nil || finalErr != nil || !os.SameFile(dataInfo, finalInfo) {
				return errors.New("unregistered media ownership cannot be verified")
			}
			if err = os.Remove(final); err != nil {
				return err
			}
			if err = syncDirectory(filepath.Dir(final)); err != nil {
				return err
			}
		}
	}
	if dataExists {
		if err = os.Remove(dataPath); err != nil {
			return err
		}
	}
	if err = os.Remove(filepath.Join(dir, "intent.json")); err != nil {
		return err
	}
	if err = os.Remove(dir); err != nil {
		return err
	}
	return syncDirectory(filepath.Join(m.root, stagingName))
}

// Sweep advances a directory cursor, bounding work and memory even when an
// earlier malformed/active intent cannot be cleaned. A later pass retries it.
func (m *Manager) Sweep(ctx context.Context, limit int) error {
	if limit < 1 || limit > 256 {
		limit = 256
	}
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil
	}
	if m.scan == nil {
		if err := ensureDirectory(filepath.Join(m.root, stagingName)); err != nil {
			return err
		}
		var err error
		m.scan, err = os.Open(filepath.Join(m.root, stagingName))
		if err != nil {
			return err
		}
	}
	entries, err := m.scan.ReadDir(limit)
	if errors.Is(err, io.EOF) {
		m.scan.Close()
		m.scan = nil
	} else if err != nil {
		return err
	}
	var first error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.cleanup(ctx, entry.Name()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CleanupLegacy is a bounded startup-only sweep. The current upload writer
// never creates .upload-* files. Bare UUID files have no ownership proof and
// are reported without identifiers when unregistered, never removed.
func (m *Manager) CleanupLegacy(ctx context.Context, limit int) error {
	if limit < 1 || limit > 256 {
		limit = 256
	}
	root, err := os.Open(m.root)
	if err != nil {
		return err
	}
	defer root.Close()
	remaining := limit
	for remaining > 0 {
		entries, err := root.ReadDir(1)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		remaining--
		entry := entries[0]
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		if err = m.cleanupLegacyDirectory(ctx, entry.Name(), &remaining); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) cleanupLegacyDirectory(ctx context.Context, conn string, remaining *int) error {
	dir := filepath.Join(m.root, conn)
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	for *remaining > 0 {
		entries, err := f.ReadDir(1)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		*remaining--
		name := entries[0].Name()
		final := validID(name)
		if (!final && !strings.HasPrefix(name, ".upload-")) || !entries[0].Type().IsRegular() {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(conn, name))
		path := filepath.Join(dir, name)
		mediaID := ""
		if final {
			mediaID = name
		}
		registered, err := m.lookup(ctx, conn, mediaID, rel, path)
		if err != nil {
			return errors.New("legacy media registration cannot be verified")
		}
		if registered {
			continue
		}
		if final {
			if m.Diagnostic != nil {
				m.Diagnostic("MEDIA_ORPHAN_UNVERIFIED")
			}
			continue
		}
		if ok, err := regularOrAbsent(path); err != nil {
			return err
		} else if ok {
			if err = os.Remove(path); err != nil {
				return err
			}
			if err = syncDirectory(dir); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := m.Sweep(pass, 256); err != nil {
				m.diagnostic()
			}
			cancel()
		}
	}
}

func (m *Manager) Close() error {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if len(m.active) != 0 {
		return errors.New("media manager still has active uploads")
	}
	m.closed = true
	if m.scan != nil {
		m.scan.Close()
		m.scan = nil
	}
	if err := unix.Flock(int(m.lock.Fd()), unix.LOCK_UN); err != nil {
		return err
	}
	return m.lock.Close()
}
