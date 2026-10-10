package storage

import (
	"context"

	"github.com/mimalef70/goomni/src/domains"
)

// DeliveryCounts reads all active connection queues in one database query.
// It never reads payloads or encrypted credentials. In-flight and retry work is
// counted as pending; delivered/cancelled audit records are not pending work.
func (s *Store) DeliveryCounts(ctx context.Context) (map[string]domains.DeliveryCounts, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.connection_id,
 SUM(CASE WHEN l.state IN ('queued','retry','delivering') THEN 1 ELSE 0 END),
 SUM(CASE WHEN l.state='failed' THEN 1 ELSE 0 END),
 SUM(CASE WHEN l.state='paused' THEN 1 ELSE 0 END)
 FROM deliveries l JOIN devices d ON d.connection_id=l.connection_id
 WHERE d.deleted_at IS NULL AND l.state IN ('queued','retry','delivering','failed','paused')
 GROUP BY l.connection_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]domains.DeliveryCounts)
	for rows.Next() {
		var conn string
		var counts domains.DeliveryCounts
		if err := rows.Scan(&conn, &counts.Pending, &counts.Failed, &counts.Paused); err != nil {
			return nil, err
		}
		result[conn] = counts
	}
	return result, rows.Err()
}

func validDeliveryState(state string) bool {
	switch state {
	case "", "queued", "retry", "delivering", "delivered", "failed", "paused", "cancelled":
		return true
	}
	return false
}
