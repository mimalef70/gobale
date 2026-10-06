package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/mediafile"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/mimalef70/gobale/src/usecase"
	"github.com/stretchr/testify/require"
)

func TestUploadedMediaSurvivesBackupRestoreToDifferentRoot(t *testing.T) {
	s, svc := setupAPI(t, "")
	d, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	fixture := []byte("portable media fixture")
	r := httptest.NewRequest("POST", "/media", bytes.NewReader(fixture))
	r.SetBasicAuth("test", "password")
	r.Header.Set("X-Device-Id", d.ID)
	r.Header.Set("X-Filename", "test.txt")
	res, err := s.App.Test(r)
	require.NoError(t, err)
	require.Equal(t, 201, res.StatusCode)
	var envelope struct {
		Results domains.Media `json:"results"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&envelope))
	res.Body.Close()
	media, err := s.store.GetMedia(context.Background(), d.ConnectionID, envelope.Results.ID)
	require.NoError(t, err)
	require.False(t, filepath.IsAbs(media.Path))
	oldPath, err := mediafile.Resolve(s.opts.MediaRoot, media.Path)
	require.NoError(t, err)
	data, err := os.ReadFile(oldPath)
	require.NoError(t, err)

	restoredRoot := t.TempDir()
	backup := filepath.Join(restoredRoot, "restored.db")
	require.NoError(t, s.store.Backup(context.Background(), backup))
	newMediaRoot := filepath.Join(restoredRoot, "different-media-location")
	newPath := filepath.Join(newMediaRoot, filepath.FromSlash(media.Path))
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0700))
	require.NoError(t, os.WriteFile(newPath, data, 0600))
	// The old location disappears, proving the restored response uses new root.
	require.NoError(t, os.Rename(s.opts.MediaRoot, s.opts.MediaRoot+"-old"))
	st, err := storage.Open(backup, bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	restoredService := usecase.New(st, usecase.Options{}, func(domains.Device) domains.Client { return &testClient{} })
	restored, err := New(restoredService, st, Options{BasicAuth: "test:password", MediaRoot: newMediaRoot})
	require.NoError(t, err)
	r = httptest.NewRequest("GET", "/media/"+media.ID, nil)
	r.SetBasicAuth("test", "password")
	r.Header.Set("X-Device-Id", d.ID)
	res, err = restored.App.Test(r)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	got, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, fixture, got)
}
