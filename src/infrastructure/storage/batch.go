package storage

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
)

const maxBatchEvents = domains.MaxBatchEvents
const maxBatchBytes = 16 << 20
const maxCheckpointBytes = 1 << 20

func validateCheckpointScope(scope string) error {
	if scope == "" || len(scope) > 256 || !utf8.ValidString(scope) {
		return domains.E("INVALID_CHECKPOINT", "checkpoint scope must contain 1–256 UTF-8 bytes", 400)
	}
	for _, c := range scope {
		if c < 32 || c == 127 {
			return domains.E("INVALID_CHECKPOINT", "checkpoint scope contains control characters", 400)
		}
	}
	return nil
}

func validateBatch(batch domains.EventBatch) error {
	if len(batch.Events) > maxBatchEvents || len(batch.Checkpoints) > maxBatchEvents || len(batch.Proofs) > maxBatchEvents {
		return domains.E("INVALID_EVENT_BATCH", "provider batch exceeds the item limit", 502)
	}
	bytes := 0
	for _, event := range batch.Events {
		if event.ID == "" || len(event.ID) > 1024 || event.Type == "" || len(event.Type) > 128 || event.Checkpoint != "" {
			return domains.E("INVALID_EVENT_BATCH", "batch events require stable identities and separate checkpoint transitions", 502)
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return domains.E("INVALID_EVENT_BATCH", "provider batch contains invalid event data", 502)
		}
		bytes += len(raw)
		if event.Media != nil {
			bytes += len(event.Media.Data) + len(event.Media.AccessHash)
		}
		if event.MediaRevision != nil {
			bytes += len(event.MediaRevision.Scope) + 8
		}
		if bytes > maxBatchBytes {
			return domains.E("INVALID_EVENT_BATCH", "provider batch exceeds the byte limit", 502)
		}
	}
	seen := make(map[string]bool, len(batch.Checkpoints))
	for _, cp := range batch.Checkpoints {
		if err := validateCheckpointScope(cp.Scope); err != nil {
			return err
		}
		if seen[cp.Scope] || cp.Next == "" || len(cp.Next) > maxCheckpointBytes || len(cp.Expected) > maxCheckpointBytes || !utf8.ValidString(cp.Next) || !utf8.ValidString(cp.Expected) {
			return domains.E("INVALID_CHECKPOINT", "checkpoint transition is invalid or duplicated", 502)
		}
		seen[cp.Scope] = true
		bytes += len(cp.Next) + len(cp.Expected)
		if bytes > maxBatchBytes {
			return domains.E("INVALID_EVENT_BATCH", "provider batch exceeds the byte limit", 502)
		}
	}
	return nil
}

// AppendBatch commits provider page events, private references, delivery records,
// reviewed acceptance proofs and scoped cursors together. The returned count is
// zero on any failure, including commit failure; an uncertain commit is an error.
func (s *Store) AppendBatch(ctx context.Context, conn string, batch domains.EventBatch, targets []WebhookTarget) (created int, err error) {
	start := time.Now()
	defer func() { s.observePersistence("append_batch", start, err) }()
	if err = validateBatch(batch); err != nil {
		return 0, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer s.rollbackTx(tx)
	d, err := s.scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE connection_id=? AND deleted_at IS NULL`, conn))
	if err != nil {
		return 0, err
	}
	for _, cp := range batch.Checkpoints {
		current, err := checkpointValueTx(ctx, tx, conn, cp.Scope)
		if err != nil {
			return 0, err
		}
		if current != cp.Expected {
			return 0, domains.E("CHECKPOINT_CONFLICT", "provider checkpoint changed before this batch was accepted", 409)
		}
	}
	acceptedIDs := make(map[string]string, len(batch.Events))
	for _, event := range batch.Events {
		inserted, err := s.appendEventTx(ctx, tx, d, event, targets, false)
		if err != nil {
			return 0, err
		}
		if inserted {
			created++
		}
		acceptedIDs[event.ID] = scopedEventID(conn, event)
	}
	for _, proof := range batch.Proofs {
		id, present := acceptedIDs[proof.EventID]
		if !present || proof.Provider != d.Provider || acceptancePolicy(proof.Provider, proof.Kind) == nil || proof.RequestID == "" {
			return 0, domains.E("INVALID_ACCEPTANCE_PROOF", "acceptance proof is not supported for this connection and batch", 502)
		}
		var body string
		if err = tx.QueryRowContext(ctx, `SELECT body FROM events WHERE connection_id=? AND id=?`, conn, id).Scan(&body); err != nil {
			return 0, err
		}
		event, err := storedMessageProof(body)
		if err != nil {
			return 0, err
		}
		// A changed duplicate may never inject evidence missing from its original
		// durable body. Correlation is by exact account/peer/request identity only.
		if event.MessageID != proof.RequestID {
			continue
		}
		if _, err = s.reconcileOwnMessageTx(ctx, tx, conn, d.AccountID, event); err != nil {
			return 0, err
		}
	}
	for _, cp := range batch.Checkpoints {
		if err = setCheckpointTx(ctx, tx, conn, cp.Scope, cp.Next); err != nil {
			return 0, err
		}
	}
	if err = s.commitTx(tx); err != nil {
		return 0, err
	}
	return created, nil
}
