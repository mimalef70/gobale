package cmd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mimalef70/goomni/src/config"
	"github.com/stretchr/testify/require"
)

func TestUIStartupValidationPrecedesStorageOwnership(t *testing.T) {
	// Missing assets fail first in a Go-only checkout. In a built checkout the
	// invalid public origin fails. Neither path may touch the configured database.
	database := filepath.Join(t.TempDir(), "untouched", "gateway.db")
	err := run(context.Background(), config.Settings{
		UIEnabled: true, UIPublicOrigin: "http://external.example.test",
		BasicAuth: "admin:synthetic", Database: database, MasterKey: make([]byte, 32),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "UI")
	require.NoDirExists(t, filepath.Dir(database))
}
