package rest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/mimalef70/gobale/src/usecase"
	"github.com/stretchr/testify/require"
)

func TestMetricsKeepsCachedSnapshotWhenStorageFails(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "test.db")
	st, err := storage.Open(databasePath, bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	svc := usecase.New(st, usecase.Options{}, nil)
	s, err := New(svc, st, Options{BasicAuth: "test:password", MediaRoot: t.TempDir()})
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if !closed {
			require.NoError(t, st.Close())
		}
	})
	_, err = svc.CreateDevice(context.Background(), "private-alias")
	require.NoError(t, err)
	scanner := &mediaScanner{}
	defer scanner.close()
	s.sampleMetrics(context.Background(), scanner)
	stamp := s.metricSnapshot().queueAt
	require.False(t, stamp.IsZero())
	require.NoError(t, s.store.Close())
	closed = true
	// Lose the DB path as well: the media filesystem sample must remain fresh.
	require.NoError(t, os.Remove(databasePath))
	s.sampleMetrics(context.Background(), scanner)
	require.Equal(t, stamp, s.metricSnapshot().queueAt)
	require.True(t, s.metricSnapshot().diskFailed)
	require.False(t, s.metricSnapshot().mediaDiskFailed)
	require.Positive(t, s.metricSnapshot().mediaFreeBytes)
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.SetBasicAuth("test", "password")
	res, err := s.App.Test(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	body := string(raw)
	require.Contains(t, body, "gobale_devices 1")
	require.Contains(t, body, `gobale_metrics_snapshot_stale{component="queue"} 1`)
	require.Contains(t, body, `gobale_metrics_snapshot_stale{component="disk"} 1`)
	require.Contains(t, body, `gobale_metrics_snapshot_stale{component="media_disk"} 0`)
	require.Contains(t, body, "gobale_db_pool_wait_seconds_total")
	require.NotContains(t, body, "private-alias")
	require.NotContains(t, body, s.opts.MediaRoot)
	req = httptest.NewRequest("GET", "/ready", nil)
	req.SetBasicAuth("test", "password")
	res, err = s.App.Test(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 503, res.StatusCode)
}

func TestMetricsNeverScansMediaAtScrapeTime(t *testing.T) {
	s, _ := setupAPI(t, "")
	s.operational.snapshot.mediaAt = time.Now()
	s.operational.snapshot.mediaBytes = 123
	s.operational.snapshot.mediaFreeBytes = 456
	s.opts.MediaRoot = "/does-not-exist/private-root"
	request := httptest.NewRequest("GET", "/metrics", nil)
	request.SetBasicAuth("test", "password")
	res, err := s.App.Test(request)
	require.NoError(t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Contains(t, string(raw), "gobale_media_bytes 123")
	require.Contains(t, string(raw), "gobale_media_free_bytes 456")
	require.NotContains(t, string(raw), "private-root")
}

func TestMissingMediaFilesystemKeepsPreviousCapacityWithoutStalingDatabase(t *testing.T) {
	s, _ := setupAPI(t, "")
	s.opts.MediaRoot = t.TempDir()
	scanner := &mediaScanner{}
	defer scanner.close()
	s.sampleMetrics(context.Background(), scanner)
	before := s.metricSnapshot()
	require.Positive(t, before.mediaFreeBytes)
	require.False(t, before.mediaDiskAt.IsZero())
	require.NoError(t, os.Remove(s.opts.MediaRoot))
	s.sampleMetrics(context.Background(), scanner)
	after := s.metricSnapshot()
	require.True(t, after.mediaDiskFailed)
	require.False(t, after.diskFailed)
	require.Equal(t, before.mediaFreeBytes, after.mediaFreeBytes)
	require.Equal(t, before.mediaDiskAt, after.mediaDiskAt)
	require.True(t, after.diskAt.After(before.diskAt))
	request := httptest.NewRequest("GET", "/metrics", nil)
	request.SetBasicAuth("test", "password")
	res, err := s.App.Test(request)
	require.NoError(t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Contains(t, string(raw), `gobale_metrics_snapshot_stale{component="disk"} 0`)
	require.Contains(t, string(raw), `gobale_metrics_snapshot_stale{component="media_disk"} 1`)
	require.NotContains(t, string(raw), s.opts.MediaRoot)
}

func TestMediaMetricsScanIsIncrementalAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	for n := 0; n < 2100; n++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprint(n)), []byte("x"), 0600))
	}
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "private"), []byte(strings.Repeat("x", 100)), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "skip")))
	scan := &mediaScanner{}
	defer scan.close()
	_, complete, err := scan.step(context.Background(), root)
	require.NoError(t, err)
	require.False(t, complete)
	require.LessOrEqual(t, len(scan.stack), 16)
	total, complete, err := scan.step(context.Background(), root)
	require.NoError(t, err)
	require.True(t, complete)
	require.EqualValues(t, 2100, total)
	require.Empty(t, scan.stack)
}
