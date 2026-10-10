package cmd

import (
	"github.com/mimalef70/goomni/src/config"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestInitPublicWebIdentityRequiresOptIn(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, initializeWithClient(dir, true))
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	require.Contains(t, string(b), "BALE_APP_ID=4\n")
	require.Contains(t, string(b), "BALE_API_KEY="+config.PublicWebAPIKey+"\n")
}

func TestInitPrivateAndNeverOverwrite(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, initialize(dir))
	for _, name := range []string{".env", "master.key"} {
		p := filepath.Join(dir, name)
		before, e := os.ReadFile(p)
		require.NoError(t, e)
		info, e := os.Stat(p)
		require.NoError(t, e)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
		require.Error(t, initialize(dir))
		after, e := os.ReadFile(p)
		require.NoError(t, e)
		require.Equal(t, before, after)
	}
}
