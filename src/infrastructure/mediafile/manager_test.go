package mediafile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/stretchr/testify/require"
)

func TestManagerProtectsActiveAndRegisteredUploads(t *testing.T) {
	ctx := context.Background()
	registered := false
	m, err := Open(t.TempDir(), func(context.Context, string, string, string, string) (bool, error) { return registered, nil })
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	u, err := m.Begin(ctx, uuid.NewString())
	require.NoError(t, err)
	_, err = u.File.Write([]byte("synthetic-media"))
	require.NoError(t, err)
	require.NoError(t, m.Sweep(ctx, 1))
	require.FileExists(t, filepath.Join(u.dir, "data"))
	require.Error(t, m.Close(), "an active upload must retain root ownership")
	_, err = Open(m.root, m.lookup)
	require.Error(t, err)
	require.NoError(t, u.Publish())
	require.NoError(t, m.Sweep(ctx, 10))
	require.FileExists(t, filepath.Join(m.root, u.RelativePath()))
	registered = true
	u.Release()
	u.Release()
	require.FileExists(t, filepath.Join(m.root, u.RelativePath()))
	require.NoDirExists(t, u.dir)
}

func TestManagerRetainsIntentOnAmbiguousDatabaseFailure(t *testing.T) {
	ctx := context.Background()
	lookupErr := errors.New("synthetic database unavailable")
	registered := false
	m, err := Open(t.TempDir(), func(context.Context, string, string, string, string) (bool, error) { return registered, lookupErr })
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	u, err := m.Begin(ctx, uuid.NewString())
	require.NoError(t, err)
	_, err = u.File.Write([]byte("synthetic-media"))
	require.NoError(t, err)
	require.NoError(t, u.Publish())
	u.Release()
	require.DirExists(t, u.dir)
	require.FileExists(t, filepath.Join(m.root, u.RelativePath()))
	// A registration can have committed even though its caller saw an error.
	lookupErr, registered = nil, true
	require.NoError(t, m.Sweep(ctx, 256))
	require.NoDirExists(t, u.dir)
	require.FileExists(t, filepath.Join(m.root, u.RelativePath()))
}

func TestManagerRemovesOnlyOwnedUnregisteredFiles(t *testing.T) {
	ctx := context.Background()
	m, err := Open(t.TempDir(), func(context.Context, string, string, string, string) (bool, error) { return false, nil })
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	u, err := m.Begin(ctx, uuid.NewString())
	require.NoError(t, err)
	_, err = u.File.Write([]byte("synthetic-media"))
	require.NoError(t, err)
	require.NoError(t, u.Publish())
	unknown := filepath.Join(m.root, u.v.ConnectionID, uuid.NewString())
	require.NoError(t, os.WriteFile(unknown, []byte("unproven-orphan"), 0600))
	u.Release()
	require.NoFileExists(t, filepath.Join(m.root, u.RelativePath()))
	require.NoDirExists(t, u.dir)
	require.FileExists(t, unknown)
}

func TestManagerRejectsSymlinkAndMalformedOwnership(t *testing.T) {
	ctx := context.Background()
	m, err := Open(t.TempDir(), func(context.Context, string, string, string, string) (bool, error) { return false, nil })
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	u, err := m.Begin(ctx, uuid.NewString())
	require.NoError(t, err)
	outside := filepath.Join(t.TempDir(), "private-fixture")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(m.root, u.v.ConnectionID), 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(m.root, u.RelativePath())))
	require.Error(t, u.Publish())
	u.Release()
	require.FileExists(t, outside)
	require.DirExists(t, u.dir)
	require.Error(t, m.Sweep(ctx, 256))
	// A malformed intent cannot introduce paths outside the private root.
	require.NoError(t, os.WriteFile(filepath.Join(u.dir, "intent.json"), []byte(`{"connection_id":"../escape","media_id":"`+u.ID()+`"}`), 0600))
	_ = m.Sweep(ctx, 256) // close the current directory cursor at EOF
	require.Error(t, m.Sweep(ctx, 256))
	require.FileExists(t, outside)
}

func TestPublicationCollisionCannotDeleteAnUnownedFinalFile(t *testing.T) {
	m, err := Open(t.TempDir(), func(context.Context, string, string, string, string) (bool, error) { return false, nil })
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	u, err := m.Begin(context.Background(), uuid.NewString())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(m.root, u.v.ConnectionID), 0700))
	path := filepath.Join(m.root, u.RelativePath())
	require.NoError(t, os.WriteFile(path, []byte("preexisting unrelated file"), 0600))
	require.Error(t, u.Publish())
	u.Release()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "preexisting unrelated file", string(content))
	require.DirExists(t, u.dir)
}

func TestSweepBoundDoesNotStarveLaterIntents(t *testing.T) {
	ctx := context.Background()
	lookupErr := errors.New("synthetic storage failure")
	m, err := Open(t.TempDir(), func(context.Context, string, string, string, string) (bool, error) { return false, lookupErr })
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	uploads := make([]*Upload, 3)
	for i := range uploads {
		uploads[i], err = m.Begin(ctx, uuid.NewString())
		require.NoError(t, err)
		uploads[i].Release()
	}
	require.NoError(t, os.WriteFile(filepath.Join(uploads[0].dir, "intent.json"), []byte("malformed"), 0600))
	lookupErr = nil
	for i := 0; i < 5; i++ {
		_ = m.Sweep(ctx, 1)
	}
	require.DirExists(t, uploads[0].dir)
	require.NoDirExists(t, uploads[1].dir)
	require.NoDirExists(t, uploads[2].dir)
}

func TestLegacySweepIsNarrowAndKeepsRegisteredFiles(t *testing.T) {
	ctx := context.Background()
	conn := uuid.NewString()
	m, err := Open(t.TempDir(), func(_ context.Context, _, _ string, relative, _ string) (bool, error) {
		return filepath.Base(relative) == ".upload-registered", nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	dir := filepath.Join(m.root, conn)
	require.NoError(t, os.Mkdir(dir, 0700))
	for _, name := range []string{".upload-orphan", ".upload-registered", "arbitrary", uuid.NewString()} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("synthetic"), 0600))
	}
	require.NoError(t, m.CleanupLegacy(ctx, 256))
	require.NoFileExists(t, filepath.Join(dir, ".upload-orphan"))
	require.FileExists(t, filepath.Join(dir, ".upload-registered"))
	require.FileExists(t, filepath.Join(dir, "arbitrary"))
}

func TestLegacyFinalOrphansAreReportedWithoutRemovalOrIdentifiers(t *testing.T) {
	for _, test := range []struct {
		name       string
		registered bool
		lookupErr  error
	}{
		{name: "unregistered"},
		{name: "registered", registered: true},
		{name: "ambiguous database", lookupErr: errors.New("synthetic database error")},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn, id := uuid.NewString(), uuid.NewString()
			var lookupCalls int
			m, err := Open(t.TempDir(), func(_ context.Context, gotConn, gotID, relative, absolute string) (bool, error) {
				lookupCalls++
				require.Equal(t, conn, gotConn)
				require.Equal(t, id, gotID)
				require.Equal(t, filepath.ToSlash(filepath.Join(conn, id)), relative)
				require.Equal(t, id, filepath.Base(absolute))
				return test.registered, test.lookupErr
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, m.Close()) })
			dir := filepath.Join(m.root, conn)
			require.NoError(t, os.Mkdir(dir, 0700))
			path := filepath.Join(dir, id)
			require.NoError(t, os.WriteFile(path, []byte("synthetic private content"), 0600))
			// Unknown files and UUID-shaped symlinks are not ownership evidence.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "unknown-file"), []byte("synthetic"), 0600))
			require.NoError(t, os.Symlink(path, filepath.Join(dir, uuid.NewString())))
			var diagnostics []string
			m.Diagnostic = func(code string) { diagnostics = append(diagnostics, code) }
			err = m.CleanupLegacy(context.Background(), 256)
			if test.lookupErr != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, lookupCalls)
			if !test.registered && test.lookupErr == nil {
				require.Equal(t, []string{"MEDIA_ORPHAN_UNVERIFIED"}, diagnostics)
			} else {
				require.Empty(t, diagnostics)
			}
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "synthetic private content", string(body))
		})
	}
}

func TestLegacyOrphanDiagnosticsRespectStartupBound(t *testing.T) {
	lookups, diagnostics := 0, 0
	m, err := Open(t.TempDir(), func(context.Context, string, string, string, string) (bool, error) {
		lookups++
		return false, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	dir := filepath.Join(m.root, uuid.NewString())
	require.NoError(t, os.Mkdir(dir, 0700))
	for range 12 {
		require.NoError(t, os.WriteFile(filepath.Join(dir, uuid.NewString()), []byte("synthetic"), 0600))
	}
	m.Diagnostic = func(code string) {
		require.Equal(t, "MEDIA_ORPHAN_UNVERIFIED", code)
		diagnostics++
	}
	require.NoError(t, m.CleanupLegacy(context.Background(), 6))
	require.Positive(t, diagnostics)
	require.LessOrEqual(t, diagnostics, 5, "the connection directory also consumes an entry")
	require.Equal(t, lookups, diagnostics)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 12)
}

// Each child exits without defers, exercising OS-released locks and real SQLite
// commit boundaries rather than approximating a crash by closing Manager.
func TestMediaProcessCrashRecovery(t *testing.T) {
	if phase := os.Getenv("GOBALE_MEDIA_TEST_CRASH"); phase != "" {
		mediaCrashChild(t, phase, os.Getenv("GOBALE_MEDIA_TEST_ROOT"))
		return
	}
	for _, phase := range []string{"staged", "published", "registered", "deleted-connection"} {
		t.Run(phase, func(t *testing.T) {
			base := t.TempDir()
			command := exec.Command(os.Args[0], "-test.run=^TestMediaProcessCrashRecovery$")
			command.Env = append(os.Environ(), "GOBALE_MEDIA_TEST_CRASH="+phase, "GOBALE_MEDIA_TEST_ROOT="+base)
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, string(output))
			require.Equal(t, 91, exit.ExitCode(), string(output))
			store, err := storage.Open(filepath.Join(base, "fixture.db"), bytes.Repeat([]byte{7}, 32))
			require.NoError(t, err)
			defer store.Close()
			m, err := Open(filepath.Join(base, "media"), store.MediaFileRegistered)
			require.NoError(t, err)
			defer m.Close()
			entries, err := os.ReadDir(filepath.Join(m.root, stagingName))
			require.NoError(t, err)
			require.Len(t, entries, 1)
			body, err := os.ReadFile(filepath.Join(m.root, stagingName, entries[0].Name(), "intent.json"))
			require.NoError(t, err)
			var v intent
			require.NoError(t, json.Unmarshal(body, &v))
			require.NoError(t, m.Sweep(context.Background(), 256))
			require.NoDirExists(t, filepath.Join(m.root, stagingName, v.MediaID))
			path := filepath.Join(m.root, v.ConnectionID, v.MediaID)
			if phase == "registered" || phase == "deleted-connection" {
				content, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "synthetic crash media", string(content))
			} else {
				require.NoFileExists(t, path)
			}
		})
	}
}

func mediaCrashChild(t *testing.T, phase, base string) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(base, "fixture.db"), bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	d, err := store.CreateDevice(ctx, "synthetic")
	require.NoError(t, err)
	m, err := Open(filepath.Join(base, "media"), store.MediaFileRegistered)
	require.NoError(t, err)
	u, err := m.Begin(ctx, d.ConnectionID)
	require.NoError(t, err)
	_, err = u.File.Write([]byte("synthetic crash media"))
	require.NoError(t, err)
	require.NoError(t, u.File.Sync())
	if phase != "staged" {
		require.NoError(t, u.Publish())
	}
	if phase == "registered" || phase == "deleted-connection" {
		require.NoError(t, store.SaveMedia(ctx, domains.Media{ID: u.ID(), ConnectionID: d.ConnectionID, Name: "fixture.txt", ContentType: "text/plain", Size: 21, Path: u.RelativePath()}))
	}
	if phase == "deleted-connection" {
		require.NoError(t, store.DeleteDevice(ctx, d.ConnectionID))
	}
	os.Exit(91)
}
