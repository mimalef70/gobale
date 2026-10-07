package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestSQLiteFullAndReadOnlyNeverAcceptOrAdvanceCheckpoint(t *testing.T) {
	for _, mode := range []string{"full", "readonly"} {
		t.Run(mode, func(t *testing.T) {
			st, _ := testStore(t)
			ctx := context.Background()
			d := device(t, st, "failure")
			_, err := st.db.Exec(`UPDATE devices SET checkpoint='before' WHERE connection_id=?`, d.ConnectionID)
			require.NoError(t, err)
			if mode == "full" {
				var pages int
				require.NoError(t, st.db.QueryRow(`PRAGMA page_count`).Scan(&pages))
				_, err = st.db.Exec(fmt.Sprintf(`PRAGMA max_page_count=%d`, pages))
				require.NoError(t, err)
			} else {
				_, err = st.db.Exec(`PRAGMA query_only=ON`)
				require.NoError(t, err)
			}
			payload, _ := json.Marshal(map[string]string{"kind": "text", "message": strings.Repeat("x", 1<<20)})
			accepted, err := st.AppendEvent(ctx, d.ConnectionID, domains.Event{ID: "failed-event", Type: "message", Peer: domains.Peer{Type: "user", ID: "77"}, Time: time.Now(), Payload: payload, Checkpoint: "after"}, []WebhookTarget{{URL: "https://synthetic.invalid", Secret: "synthetic"}})
			require.Error(t, err)
			require.False(t, accepted)
			cp, err := st.Checkpoint(ctx, d.ConnectionID)
			require.NoError(t, err)
			require.Equal(t, "before", cp)
			var events, deliveries int
			require.NoError(t, st.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events))
			require.NoError(t, st.db.QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&deliveries))
			require.Zero(t, events)
			require.Zero(t, deliveries)
			_, _, err = st.Enqueue(ctx, d.ConnectionID, domains.SendRequest{Kind: "text", Peer: domains.Peer{Type: "user", ID: "77"}, Text: strings.Repeat("x", 1<<20)}, "failed", 1000)
			require.Error(t, err)
			var operations int
			require.NoError(t, st.db.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&operations))
			require.Zero(t, operations)
		})
	}
}
