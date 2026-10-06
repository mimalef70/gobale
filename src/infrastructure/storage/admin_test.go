package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func TestDeliveryAdminFiltersCountsAndPayloadProjection(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "admin")
	other := device(t, s, "other")
	states := []string{"queued", "retry", "delivering", "delivered", "failed", "paused", "cancelled"}
	for i, state := range states {
		_, err := s.AppendEvent(ctx, d.ConnectionID, event(fmt.Sprintf("event-%d", i), ""), []WebhookTarget{{URL: fmt.Sprintf("https://example.test/%d", i), Secret: "synthetic"}})
		require.NoError(t, err)
		_, err = s.db.Exec(`UPDATE deliveries SET state=? WHERE url=?`, state, fmt.Sprintf("https://example.test/%d", i))
		require.NoError(t, err)
	}
	_, err := s.AppendEvent(ctx, other.ConnectionID, event("other", ""), []WebhookTarget{{URL: "https://other.test/hook", Secret: "synthetic"}})
	require.NoError(t, err)
	counts, err := s.DeliveryCounts(ctx)
	require.NoError(t, err)
	require.Equal(t, domains.DeliveryCounts{Pending: 3, Failed: 1, Paused: 1}, counts[d.ConnectionID])
	require.EqualValues(t, 1, counts[other.ConnectionID].Pending)
	for _, state := range states {
		got, err := s.ListDeliveriesFiltered(ctx, d.ConnectionID, 25, 0, state, false)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, state, got[0].State)
		require.JSONEq(t, `null`, string(got[0].Body))
		raw, err := json.Marshal(got)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "hello")
		require.NotContains(t, string(raw), "synthetic")
	}
	all, err := s.ListDeliveries(ctx, d.ConnectionID, 25, 0)
	require.NoError(t, err)
	require.Len(t, all, len(states))
	require.Contains(t, string(all[0].Body), "hello")
	page, err := s.ListDeliveriesFiltered(ctx, d.ConnectionID, 2, 2, "", false)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, all[2].ID, page[0].ID)
	_, err = s.ListDeliveriesFiltered(ctx, d.ConnectionID, 25, 0, "queued' OR 1=1 --", false)
	errorCode(t, err, "INVALID_DELIVERY_STATE")
	// Suppressing payload must also suppress reading it from SQLite, not just
	// remove a potentially huge/invalid value after the query returns.
	_, err = s.db.Exec(`UPDATE deliveries SET body=? WHERE connection_id=?`, strings.Repeat("x", 1<<20), d.ConnectionID)
	require.NoError(t, err)
	got, err := s.ListDeliveriesFiltered(ctx, d.ConnectionID, 25, 0, "", false)
	require.NoError(t, err)
	require.Len(t, got, len(states))
	require.Equal(t, "null", string(got[0].Body))
	require.NoError(t, s.DeleteDevice(ctx, other.ConnectionID))
	counts, err = s.DeliveryCounts(ctx)
	require.NoError(t, err)
	_, exists := counts[other.ConnectionID]
	require.False(t, exists)
	_, err = s.ListDeliveriesFiltered(ctx, other.ConnectionID, 25, 0, "", false)
	errorCode(t, err, "NOT_FOUND")
}

func TestDeviceInstanceTokenPersistsAndDoesNotFollowAliasReuse(t *testing.T) {
	s, path := testStore(t)
	ctx := context.Background()
	d := device(t, s, "same")
	require.NotEmpty(t, d.InstanceID)
	require.Equal(t, d.InstanceToken(), d.InstanceID)
	require.NotEqual(t, d.ConnectionID, d.InstanceID)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	require.NotContains(t, string(raw), d.ConnectionID)
	require.Contains(t, string(raw), d.InstanceID)
	require.NoError(t, s.Close())
	s, err = Open(path, testKey)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	restored, err := s.GetDevice(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, d.InstanceID, restored.InstanceID)
	require.NoError(t, s.DeleteDevice(ctx, d.ConnectionID))
	replacement := device(t, s, d.ID)
	require.NotEqual(t, d.InstanceID, replacement.InstanceID)
}
