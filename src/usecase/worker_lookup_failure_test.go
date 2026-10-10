package usecase

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/mimalef70/goomni/src/pkg/sqlite"
	"github.com/stretchr/testify/require"
)

func TestWorkerLookupFailurePreservesAcceptedOperation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lookup.db")
	store, err := storage.Open(path, []byte(strings.Repeat("k", 32)))
	require.NoError(t, err)
	defer store.Close()
	client := &fakeClient{status: readyStatus()}
	s := New(store, Options{PollInterval: time.Millisecond}, func(domains.Device) domains.Client { return client })
	s.ctx = ctx
	defer s.Close(ctx)
	d := mustDevice(t, s, "lookup-failure")
	e, err := s.entry(d)
	require.NoError(t, err)
	e.desired = true
	op, err := s.Send(ctx, d.ID, sendReq("synthetic"), "accepted")
	require.NoError(t, err)
	claimed := claimOne(t, store)
	// The independent SQLite handle is a test-only fault injector. A malformed
	// metadata read must not be interpreted as a deleted account, and must not
	// contact the provider or discard work that was already accepted.
	fault, err := sql.Open(sqlite.DriverName, path)
	require.NoError(t, err)
	defer fault.Close()
	_, err = fault.ExecContext(ctx, `UPDATE devices SET webhook_filter='invalid-json' WHERE connection_id=?`, d.ConnectionID)
	require.NoError(t, err)
	s.processOperation(claimed)
	saved, err := store.GetOperation(ctx, d.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", saved.State)
	require.Empty(t, saved.ErrorCode)
	require.Zero(t, client.sendCalls)
	_, err = fault.ExecContext(ctx, `UPDATE devices SET webhook_filter='{}' WHERE connection_id=?`, d.ConnectionID)
	require.NoError(t, err)
	s.processOperation(claimOne(t, store))
	saved, err = store.GetOperation(ctx, d.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", saved.State)
	require.Equal(t, 1, client.sendCalls)
}
