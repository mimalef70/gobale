package mediafile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolvePortableAndLegacyPathsRejectsEscapes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "media")
	path := filepath.Join(root, "connection", "asset")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("fixture"), 0600))
	for _, stored := range []string{"connection/asset", path} {
		got, err := Resolve(root, stored)
		require.NoError(t, err)
		canonical, err := filepath.EvalSymlinks(path)
		require.NoError(t, err)
		require.Equal(t, canonical, got)
	}
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escaped")))
	for _, stored := range []string{"", ".", "../outside", outside, "escaped", "connection"} {
		_, err := Resolve(root, stored)
		require.Error(t, err, stored)
	}
}
