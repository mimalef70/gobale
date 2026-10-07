package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Reconstruct the v4 delivery layout, including its former ordering index.
// Older migration tests subsequently remove indexes absent from their version.
func restoreLegacyDeliveryOrder(t *testing.T, s *Store) {
	t.Helper()
	restoreV5APISchema(t, s)
	for _, ddl := range []string{
		`DROP INDEX events_message_proof`,
		`DROP INDEX deliveries_pending_order`,
		`DROP INDEX deliveries_queue_order`,
		`ALTER TABLE deliveries DROP COLUMN queue_order`,
		`CREATE INDEX deliveries_pending_order ON deliveries(connection_id,url,created_at) WHERE state IN ('queued','retry')`,
	} {
		_, err := s.db.Exec(ddl)
		require.NoError(t, err)
	}
}

func TestManualRetryKeepsPendingOrderAcrossRestartsAndReplay(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "ordered")
	firstTarget := WebhookTarget{URL: "https://one.test/hook", Secret: "synthetic"}
	otherTarget := WebhookTarget{URL: "https://two.test/hook", Secret: "synthetic-other"}
	_, err := s.AppendEvent(ctx, d.ConnectionID, event("A", ""), []WebhookTarget{firstTarget})
	require.NoError(t, err)
	a, err := s.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, a, 1)
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, a[0].ID, "retry", time.Now().Add(time.Hour), "temporary"))
	_, err = s.AppendEvent(ctx, d.ConnectionID, event("B", ""), []WebhookTarget{firstTarget, otherTarget})
	require.NoError(t, err)
	// Explicit equal timestamps prevent a timestamp-only fix hiding the bug.
	_, err = s.db.Exec(`UPDATE deliveries SET created_at=1234 WHERE connection_id=?`, d.ConnectionID)
	require.NoError(t, err)
	current := a[0]
	for i := range 3 {
		replacement, err := s.RetryDelivery(ctx, d.ConnectionID, current.ID)
		require.NoError(t, err)
		require.Equal(t, current.EventID, replacement.EventID)
		require.Equal(t, current.Body, replacement.Body)
		require.Zero(t, replacement.Attempts)
		old, err := s.GetDelivery(ctx, d.ConnectionID, current.ID)
		require.NoError(t, err)
		require.Equal(t, "cancelled", old.State)
		require.Equal(t, current.Attempts, old.Attempts)
		require.NoError(t, s.Close())
		s, err = Open(path, testKey)
		require.NoError(t, err)
		reopened := s
		t.Cleanup(func() { reopened.Close() })
		jobs, err := s.ClaimDeliveries(ctx, 8, time.Now().Add(time.Second))
		require.NoError(t, err)
		if i == 0 {
			require.Len(t, jobs, 2, "the independent destination must keep progressing")
		} else {
			require.Len(t, jobs, 1)
		}
		found := false
		for _, job := range jobs {
			if job.URL == firstTarget.URL {
				require.Equal(t, replacement.ID, job.ID, "pending B must never overtake retried A")
				current = job
				found = true
			} else {
				require.Equal(t, otherTarget.URL, job.URL)
				require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, job.ID, "delivered", time.Now(), ""))
			}
		}
		require.True(t, found)
		if i < 2 {
			require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, current.ID, "retry", time.Now().Add(time.Hour), "temporary"))
		}
	}
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, current.ID, "delivered", time.Now(), ""))
	// An explicit replay is newly queued work; it must not inherit A's old position.
	replayed, err := s.ReplayDelivery(ctx, d.ConnectionID, current.ID, []WebhookTarget{firstTarget})
	require.NoError(t, err)
	require.Len(t, replayed, 1)
	require.Equal(t, current.EventID, replayed[0].EventID)
	jobs, err := s.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, scopedEventID(d.ConnectionID, event("B", "")), jobs[0].EventID)
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, jobs[0].ID, "delivered", time.Now(), ""))
	jobs, err = s.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, replayed[0].ID, jobs[0].ID)
}

func TestStableDeliveryOrderMigrationPreservesTimestampAndTieOrder(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "legacy")
	var ids []string
	for _, name := range []string{"later", "first", "second"} {
		_, err := s.AppendEvent(ctx, d.ConnectionID, event(name, ""), []WebhookTarget{{URL: "https://example.test/hook", Secret: "synthetic"}})
		require.NoError(t, err)
		var id string
		require.NoError(t, s.db.QueryRow(`SELECT id FROM deliveries WHERE event_id=?`, scopedEventID(d.ConnectionID, event(name, ""))).Scan(&id))
		ids = append(ids, id)
	}
	restoreLegacyDeliveryOrder(t, s)
	_, err := s.db.Exec(`UPDATE deliveries SET created_at=100`)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE deliveries SET created_at=200 WHERE id=?`, ids[0])
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE gobale_meta SET version=4`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	upgraded, err := Open(path, testKey)
	require.NoError(t, err)
	defer upgraded.Close()
	var version int
	require.NoError(t, upgraded.db.QueryRow(`SELECT version FROM gobale_meta`).Scan(&version))
	require.Equal(t, schemaVersion, version)
	for _, expected := range []string{ids[1], ids[2], ids[0]} {
		jobs, err := upgraded.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		require.Equal(t, expected, jobs[0].ID)
		require.Equal(t, "synthetic", jobs[0].Secret, "migration must preserve encrypted routing data")
		require.NoError(t, upgraded.UpdateDelivery(ctx, d.ConnectionID, jobs[0].ID, "delivered", time.Now(), ""))
	}
}

func TestManualRetryOrderingRollbackPreservesOriginalAttempt(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "rollback")
	targets := []WebhookTarget{{URL: "https://example.test/hook"}}
	_, err := s.AppendEvent(ctx, d.ConnectionID, event("A", ""), targets)
	require.NoError(t, err)
	jobs, err := s.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	original := jobs[0]
	require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, original.ID, "retry", time.Now().Add(time.Hour), "temporary"))
	_, err = s.AppendEvent(ctx, d.ConnectionID, event("B", ""), targets)
	require.NoError(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER fail_retry_cancel BEFORE UPDATE OF state ON deliveries WHEN NEW.state='cancelled' BEGIN SELECT RAISE(ABORT,'synthetic persistence failure'); END`)
	require.NoError(t, err)
	_, err = s.RetryDelivery(ctx, d.ConnectionID, original.ID)
	require.ErrorContains(t, err, "synthetic persistence failure")
	rows, err := s.ListDeliveries(ctx, d.ConnectionID, 100, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2, "failed retry must not leave its replacement queued")
	old, err := s.GetDelivery(ctx, d.ConnectionID, original.ID)
	require.NoError(t, err)
	require.Equal(t, "retry", old.State)
	require.Equal(t, original.Attempts, old.Attempts)
	jobs, err = s.ClaimDeliveries(ctx, 1, time.Now().Add(2*time.Hour))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, original.ID, jobs[0].ID)
}
