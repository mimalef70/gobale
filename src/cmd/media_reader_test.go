package cmd

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMediaReaderRetainsSeekAndReleasesSlotOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-media")
	require.NoError(t, os.WriteFile(path, []byte("synthetic"), 0600))
	f, err := os.Open(path)
	require.NoError(t, err)
	var released int
	var source io.ReadCloser = &releaseReader{ReadSeekCloser: f, release: func() { released++ }}
	seekable, ok := source.(io.ReadSeeker)
	require.True(t, ok, "voice inspection must reuse the immutable file")
	_, err = io.ReadAll(source)
	require.NoError(t, err)
	_, err = seekable.Seek(0, io.SeekStart)
	require.NoError(t, err)
	data, err := io.ReadAll(source)
	require.NoError(t, err)
	require.Equal(t, "synthetic", string(data))
	require.NoError(t, source.Close())
	_ = source.Close()
	require.Equal(t, 1, released)
}
