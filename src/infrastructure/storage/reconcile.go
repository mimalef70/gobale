package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mimalef70/gobale/src/domains"
)

const storedMessageProofIndex = `CREATE INDEX events_message_proof ON events(connection_id,message_id) WHERE type='message'`

func storedMessageProof(body string) (domains.Event, error) {
	var event domains.Event
	if json.Unmarshal([]byte(body), &event) != nil {
		// Do not expose the stored message or malformed private field in errors.
		return domains.Event{}, errors.New("stored message proof could not be decoded")
	}
	return event, nil
}

// reconcileStoredVoiceProofs repairs pre-upgrade unknown voice sends using only
// durable own-message evidence. It runs before workers on open, never contacts
// Bale, and neither re-emits events nor modifies checkpoints/webhook deliveries.
// Batches bound candidate memory; indexed event lookups load one body at a time.
// All repairs commit together, or none do if persistence/proof decoding fails.
func (s *Store) reconcileStoredVoiceProofs(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type candidate struct {
		rowID                    int64
		connection, account, rid string
	}
	lastOperation := int64(0)
	for {
		rows, err := tx.QueryContext(ctx, `SELECT o.rowid,o.connection_id,d.account_id,json_extract(o.request,'$.request_id') FROM operations o JOIN devices d ON d.connection_id=o.connection_id
 WHERE o.rowid>? AND o.state='unknown' AND d.deleted_at IS NULL AND d.account_id<>''
 AND json_extract(o.request,'$.kind')='voice' AND COALESCE(json_extract(o.request,'$.operation'),'')=''
 AND COALESCE(json_extract(o.request,'$.request_id'),'')<>'' ORDER BY o.rowid LIMIT 100`, lastOperation)
		if err != nil {
			return err
		}
		batch := make([]candidate, 0, 100)
		for rows.Next() {
			var c candidate
			if err = rows.Scan(&c.rowID, &c.connection, &c.account, &c.rid); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return tx.Commit()
		}
		for _, c := range batch {
			lastOperation = c.rowID
			lastEvent := int64(0)
			for {
				var rowID int64
				var body string
				err = tx.QueryRowContext(ctx, `SELECT rowid,body FROM events WHERE connection_id=? AND message_id=? AND type='message' AND rowid>? ORDER BY rowid LIMIT 1`, c.connection, c.rid, lastEvent).Scan(&rowID, &body)
				if errors.Is(err, sql.ErrNoRows) {
					break
				}
				if err != nil {
					return err
				}
				lastEvent = rowID
				proof, err := storedMessageProof(body)
				if err != nil {
					return err
				}
				reconciled, err := s.reconcileOwnMessageTx(ctx, tx, c.connection, c.account, proof)
				if err != nil {
					return err
				}
				if reconciled {
					break
				}
			}
		}
	}
}
