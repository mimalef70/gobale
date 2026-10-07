package storage

import (
	"context"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

// Cleanup must find both current relative and legacy absolute registrations
// without scanning retained media on every upload release or sweep candidate.
func TestMediaRegistrationLookupUsesIndexesFreshAndMigrated(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "indexed-media")
	_, err := s.db.Exec(`WITH RECURSIVE sequence(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM sequence WHERE n<1000) INSERT INTO media(id,connection_id,name,content_type,size,path,created_at) SELECT 'synthetic-'||n,?,'fixture','application/octet-stream',1,'v1/retained-'||n,0 FROM sequence`, d.ConnectionID)
	require.NoError(t, err)
	plan := func() string {
		t.Helper()
		rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT id,connection_id,path FROM media WHERE id=? OR path=? OR path=?`, "synthetic-500", "v1/retained-500", "/synthetic/root/v1/retained-500")
		require.NoError(t, err)
		defer rows.Close()
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			details = append(details, detail)
		}
		require.NoError(t, rows.Err())
		return strings.Join(details, "\n")
	}
	check := func() {
		t.Helper()
		p := plan()
		require.Contains(t, p, "media_path")
		require.Contains(t, p, "SEARCH media")
		require.NotContains(t, p, "SCAN media")
		found, err := s.MediaFileRegistered(ctx, d.ConnectionID, "synthetic-500", "v1/retained-500", "/synthetic/root/v1/retained-500")
		require.NoError(t, err)
		require.True(t, found)
	}
	check()
	restoreV5APISchema(t, s)
	_, err = s.db.Exec(`UPDATE gobale_meta SET version=5`)
	require.NoError(t, err)
	require.Contains(t, plan(), "SCAN media", "fixture must reproduce the prior unindexed lookup")
	require.NoError(t, s.migrate(ctx))
	check()
	require.NoError(t, s.DeleteDevice(ctx, d.ConnectionID))
	check() // tombstoned accounts still protect their retained files.
}
