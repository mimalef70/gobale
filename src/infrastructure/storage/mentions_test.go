package storage

import (
	"context"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMentionIdempotencyAndSchedulePersistence(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "mention.db"), []byte(strings.Repeat("k", 32)))
	require.NoError(t, err)
	defer st.Close()
	d, err := st.CreateDevice(ctx, "mention")
	require.NoError(t, err)
	request := domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "77"}, Kind: "text", Text: "synthetic", Mentions: []string{"1"}}
	op, _, err := st.Enqueue(ctx, d.ConnectionID, request, "same", AdmissionLimits{Global: 100})
	require.NoError(t, err)
	again, created, err := st.Enqueue(ctx, d.ConnectionID, request, "same", AdmissionLimits{Global: 100})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, op.ID, again.ID)
	request.Mentions = []string{"2"}
	_, _, err = st.Enqueue(ctx, d.ConnectionID, request, "same", AdmissionLimits{Global: 100})
	require.Error(t, err)
	at := time.Now().UTC()
	schedule, err := st.CreateSchedule(ctx, d.ConnectionID, request, at)
	require.NoError(t, err)
	occurrence, err := st.MaterializeSchedule(ctx, d.ConnectionID, schedule.ID, at, nil, AdmissionLimits{Global: 100})
	require.NoError(t, err)
	require.Equal(t, []string{"2"}, occurrence.Request.Mentions)
}
