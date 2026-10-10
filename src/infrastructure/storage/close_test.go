package storage

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"github.com/mimalef70/goomni/src/domains"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Keep real SQLite persistence, but stop cancellation cleanup at its two
// asynchronous boundaries so ownership checks do not depend on scheduler luck.
type closeBarrierConnector struct {
	driver                    driver.Driver
	path                      string
	rollbackStarted, retired  chan struct{}
	allowRollback, allowClose chan struct{}
	rollbackOnce, closeOnce   sync.Once
}

func (c *closeBarrierConnector) Driver() driver.Driver { return c.driver }
func (c *closeBarrierConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	return &closeBarrierConn{Conn: conn, barrier: c}, nil
}

type closeBarrierConn struct {
	driver.Conn
	barrier *closeBarrierConnector
}

func (c *closeBarrierConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return &closeBarrierTx{Tx: tx, barrier: c.barrier}, nil
}
func (c *closeBarrierConn) ResetSession(ctx context.Context) error {
	if reset, ok := c.Conn.(driver.SessionResetter); ok {
		return reset.ResetSession(ctx)
	}
	return nil
}
func (c *closeBarrierConn) IsValid() bool {
	if valid, ok := c.Conn.(driver.Validator); ok {
		return valid.IsValid()
	}
	return true
}
func (c *closeBarrierConn) Close() error {
	c.barrier.closeOnce.Do(func() { close(c.barrier.retired) })
	<-c.barrier.allowClose
	return c.Conn.Close()
}

type closeBarrierTx struct {
	driver.Tx
	barrier *closeBarrierConnector
}

func (tx *closeBarrierTx) Rollback() error {
	tx.barrier.rollbackOnce.Do(func() { close(tx.barrier.rollbackStarted) })
	<-tx.barrier.allowRollback
	return tx.Tx.Rollback()
}

func TestCloseRetainsOwnershipThroughCancelledRollbackAndDriverClose(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "close.db")
	key := bytes.Repeat([]byte{17}, 32)
	s, err := Open(path, key)
	require.NoError(t, err)
	device, err := s.CreateDevice(ctx, "retained", domains.ProviderBale)
	require.NoError(t, err)
	barrier := &closeBarrierConnector{driver: s.db.Driver(), path: path,
		rollbackStarted: make(chan struct{}), retired: make(chan struct{}),
		allowRollback: make(chan struct{}), allowClose: make(chan struct{})}
	require.NoError(t, s.db.Close())
	s.db = sql.OpenDB(barrier)
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	var releaseRollback, releaseClose sync.Once
	unblockRollback := func() { releaseRollback.Do(func() { close(barrier.allowRollback) }) }
	unblockClose := func() { releaseClose.Do(func() { close(barrier.allowClose) }) }
	t.Cleanup(func() {
		unblockRollback()
		unblockClose()
		require.NoError(t, s.Close())
	})
	txContext, cancel := context.WithCancel(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(txContext, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(txContext, `UPDATE devices SET account_id='999' WHERE connection_id=?`, device.ConnectionID)
	require.NoError(t, err)
	cancel()
	select {
	case <-barrier.rollbackStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled transaction did not enter driver rollback")
	}
	require.ErrorIs(t, tx.Rollback(), sql.ErrTxDone, "worker returns before the driver rollback finishes")
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	require.Eventually(t, func() bool { return s.db.Stats().WaitCount > 0 }, time.Second, time.Millisecond)
	assertOwned := func() {
		t.Helper()
		next, openErr := Open(path, key)
		if next != nil {
			_ = next.Close()
		}
		require.ErrorContains(t, openErr, "already owned")
		select {
		case closeErr := <-closed:
			t.Fatalf("Close returned before driver cleanup: %v", closeErr)
		default:
		}
	}
	assertOwned()
	unblockRollback()
	select {
	case <-barrier.retired:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not reach driver retirement")
	}
	assertOwned()
	unblockClose()
	select {
	case closeErr := <-closed:
		require.NoError(t, closeErr)
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after driver retirement")
	}
	require.Zero(t, s.db.Stats().OpenConnections)
	require.NoError(t, s.Close(), "successful Close is idempotent")
	next, err := Open(path, key)
	require.NoError(t, err)
	defer next.Close()
	stored, err := next.GetDevice(ctx, device.ID)
	require.NoError(t, err)
	require.Equal(t, device.ConnectionID, stored.ConnectionID)
	require.Empty(t, stored.AccountID, "cancelled transaction must have rolled back")
}

func TestCloseTimeoutRetainsOwnershipAndCanBeRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain.db")
	key := bytes.Repeat([]byte{19}, 32)
	s, err := Open(path, key)
	require.NoError(t, err)
	defer s.Close()
	held, err := s.db.Conn(context.Background())
	require.NoError(t, err)
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, s.closeWithContext(ctx), context.DeadlineExceeded)
	other, err := Open(path, key)
	if other != nil {
		_ = other.Close()
	}
	require.ErrorContains(t, err, "already owned")
	require.NoError(t, held.Close())
	require.NoError(t, s.Ping(context.Background()), "failed drain must leave the store usable")
	require.NoError(t, s.Close())
	reopened, err := Open(path, key)
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	require.NoError(t, s.Close())
}

func TestFailedOpenCleanupWaitsForExistingConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.db")
	key := bytes.Repeat([]byte{23}, 32)
	s, err := Open(path, key)
	require.NoError(t, err)
	held, err := s.db.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := held.Close(); err != nil {
			require.ErrorIs(t, err, sql.ErrConnDone)
		}
		require.NoError(t, s.closeFailedOpen(context.Background()))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, s.closeFailedOpen(ctx), context.DeadlineExceeded)
	other, err := Open(path, key)
	if other != nil {
		_ = other.Close()
	}
	require.ErrorContains(t, err, "already owned")
	require.Equal(t, 1, s.db.Stats().OpenConnections)
	require.NoError(t, held.Close())
	require.NoError(t, s.closeFailedOpen(context.Background()))
	reopened, err := Open(path, key)
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func TestFailedOpenReleasesOwnershipAfterWrongKeyAndCorruptDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rejected.db")
	key := bytes.Repeat([]byte{29}, 32)
	s, err := Open(path, key)
	require.NoError(t, err)
	device, err := s.CreateDevice(context.Background(), "preserved", domains.ProviderBale)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = Open(path, bytes.Repeat([]byte{31}, 32))
	require.Error(t, err)
	s, err = Open(path, key)
	require.NoError(t, err)
	stored, err := s.GetDevice(context.Background(), device.ID)
	require.NoError(t, err)
	require.Equal(t, device.ConnectionID, stored.ConnectionID)
	require.NoError(t, s.Close())

	corruptPath := filepath.Join(t.TempDir(), "corrupt.db")
	require.NoError(t, os.WriteFile(corruptPath, []byte("not a sqlite database"), 0600))
	_, err = Open(corruptPath, key)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "ownership retained")
	require.NoError(t, os.Remove(corruptPath))
	s, err = Open(corruptPath, key)
	require.NoError(t, err, "failed startup must release ownership after all driver connections retire")
	require.NoError(t, s.Close())
}
