package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func deliveryPlan(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+deliveryClaimQuery, now(), 8)
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(plan, "\n")
}

func TestDeliveryIndexesExcludeRetainedHistoryAndMigrate(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	devices := make([]domains.Device, 50)
	sources := make([]string, 50)
	for i := range devices {
		devices[i] = device(t, s, fmt.Sprintf("history-%d", i))
		e := event(fmt.Sprintf("history-source-%d", i), "")
		_, err := s.AppendEvent(ctx, devices[i].ConnectionID, e, []WebhookTarget{{URL: "https://example.test/receiver"}})
		require.NoError(t, err)
		sources[i] = scopedEventID(devices[i].ConnectionID, e)
	}
	// Fifty thousand historical attempts across fifty accounts, followed by one
	// eligible pending event per account. Historical ciphertext is never read.
	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO deliveries(id,connection_id,event_id,url,secret,device_config,revision,body,state,next_at,created_at) VALUES(?,?,?,'https://example.test/receiver',X'01',0,1,'{}','delivered',0,0)`)
	require.NoError(t, err)
	for i := range 50000 {
		_, err = stmt.ExecContext(ctx, fmt.Sprintf("retained-%d", i), devices[i%50].ConnectionID, sources[i%50])
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
	restoreLegacyDeliveryOrder(t, s)
	_, err = s.db.Exec(`DROP INDEX deliveries_pending_order`)
	require.NoError(t, err)
	_, err = s.db.Exec(`DROP INDEX deliveries_inflight_target`)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE gobale_meta SET version=3`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	upgraded, err := Open(path, testKey)
	require.NoError(t, err)
	defer upgraded.Close()
	after := deliveryPlan(t, upgraded)
	t.Log("after migration:\n" + after)
	require.Contains(t, after, "deliveries_pending_order")
	require.Contains(t, after, "deliveries_inflight_target")
	var history int
	require.NoError(t, upgraded.db.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE state='delivered'`).Scan(&history))
	require.Equal(t, 50000, history, "migration must retain audit history")
	claimed, err := upgraded.ClaimDeliveries(ctx, 8, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, claimed, 8)
	seen := map[string]bool{}
	for _, job := range claimed {
		require.False(t, seen[job.ConnectionID])
		seen[job.ConnectionID] = true
	}
}

func TestReleaseDeliveryClaimRefundsPreHTTPAttemptOnly(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "a")
	_, err := s.AppendEvent(ctx, d.ConnectionID, event("source", ""), []WebhookTarget{{URL: "https://example.test/receiver"}})
	require.NoError(t, err)
	for range 10 {
		jobs, err := s.ClaimDeliveries(ctx, 1, time.Now().UTC())
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		require.Equal(t, 1, jobs[0].Attempts)
		require.NoError(t, s.ReleaseDeliveryClaim(ctx, d.ConnectionID, jobs[0].ID))
		job, err := s.GetDelivery(ctx, d.ConnectionID, jobs[0].ID)
		require.NoError(t, err)
		require.Equal(t, "retry", job.State)
		require.Zero(t, job.Attempts)
	}
	jobs, err := s.ClaimDeliveries(ctx, 1, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NoError(t, s.DeleteDevice(ctx, d.ConnectionID))
	errorCode(t, s.ReleaseDeliveryClaim(ctx, d.ConnectionID, jobs[0].ID), "DELIVERY_CONFLICT")
}

func TestDeliveryStatisticsExposePausedAndTerminalWork(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "a")
	_, err := s.AppendEvent(ctx, d.ConnectionID, event("stats", ""), []WebhookTarget{{URL: "https://one.test"}, {URL: "https://two.test"}, {URL: "https://three.test"}})
	require.NoError(t, err)
	jobs, err := s.ClaimDeliveries(ctx, 3, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, jobs, 3)
	for i, state := range []string{"delivered", "paused", "cancelled"} {
		require.NoError(t, s.UpdateDelivery(ctx, d.ConnectionID, jobs[i].ID, state, time.Now(), "test"))
	}
	stats, err := s.Stats(ctx)
	require.NoError(t, err)
	for _, metric := range []string{"webhook_delivered", "webhook_paused", "webhook_cancelled"} {
		require.Equal(t, int64(1), stats[metric], metric)
	}
	require.Zero(t, stats["webhook_pending"])
}
