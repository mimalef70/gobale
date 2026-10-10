package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mimalef70/goomni/src/domains"
)

const operationStageSchema = `CREATE TABLE operation_stages(connection_id TEXT NOT NULL REFERENCES devices(connection_id),operation_id TEXT NOT NULL REFERENCES operations(id),number INTEGER NOT NULL,name TEXT NOT NULL,state TEXT NOT NULL,private BLOB NOT NULL,started_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,PRIMARY KEY(connection_id,operation_id,number))`

type privateStage struct {
	Nonce string          `json:"nonce"`
	Data  json.RawMessage `json:"data,omitempty"`
}

func validStage(stage domains.OperationStage) bool {
	if stage.Number < 1 || stage.Number > 32 || len(stage.Nonce) > 256 || len(stage.Data) > 16384 || (len(stage.Data) > 0 && !json.Valid(stage.Data)) {
		return false
	}
	switch stage.Name {
	case "upload", "send", "forward", "poll.create", "poll.send", "profile.photo", "album.send", "message.unpin_all":
	default:
		return false
	}
	switch stage.State {
	case "started", "succeeded", "failed", "unknown":
		return true
	}
	return false
}
func (s *Store) RecordOperationStage(ctx context.Context, conn, operation, requestID string, stage domains.OperationStage) error {
	if !validStage(stage) {
		return domains.E("INVALID_OPERATION_STAGE", "invalid compound operation stage", 500)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer s.rollbackTx(tx)
	var raw, state string
	var provider domains.Provider
	err = tx.QueryRowContext(ctx, `SELECT o.request,o.state,d.provider FROM operations o JOIN devices d ON d.connection_id=o.connection_id WHERE o.connection_id=? AND o.id=? AND d.deleted_at IS NULL`, conn, operation).Scan(&raw, &state, &provider)
	if err != nil {
		return dbError(err)
	}
	var request domains.SendRequest
	if json.Unmarshal([]byte(raw), &request) != nil || request.RequestID != requestID || state != "sending" {
		return domains.E("OPERATION_CONFLICT", "operation is not the active request", 409)
	}
	aad := fmt.Sprintf("%s:operation-stage:%s:%d", conn, operation, stage.Number)
	var previousCipher []byte
	var previousName, previousState string
	err = tx.QueryRowContext(ctx, `SELECT name,state,private FROM operation_stages WHERE connection_id=? AND operation_id=? AND number=?`, conn, operation, stage.Number).Scan(&previousName, &previousState, &previousCipher)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !exists && stage.State != "started" {
		return domains.E("OPERATION_CONFLICT", "compound stage must be started before completion", 409)
	}
	if exists {
		envelope, err := s.decodePrivate(previousCipher, aad, provider)
		if err != nil {
			return err
		}
		var previous privateStage
		if json.Unmarshal(envelope.Payload, &previous) != nil {
			return domains.E("INVALID_OPERATION_STAGE", "private stage is invalid", 500)
		}
		if previousName != stage.Name || previous.Nonce != stage.Nonce || (previousState != "started" && previousState != stage.State) {
			return domains.E("OPERATION_CONFLICT", "compound stage identity or result changed", 409)
		}
	}
	plain, err := json.Marshal(privateStage{Nonce: stage.Nonce, Data: stage.Data})
	if err != nil {
		return err
	}
	cipher, err := s.encodePrivate(provider, 1, plain, aad)
	if err != nil {
		return err
	}
	timestamp := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO operation_stages(connection_id,operation_id,number,name,state,private,started_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(connection_id,operation_id,number) DO UPDATE SET state=excluded.state,private=excluded.private,updated_at=excluded.updated_at`, conn, operation, stage.Number, stage.Name, stage.State, cipher, timestamp, timestamp)
	if err != nil {
		return err
	}
	return s.commitTx(tx)
}
func (s *Store) OperationStages(ctx context.Context, conn, operation string) ([]domains.OperationStage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT number,name,state,started_at,updated_at FROM operation_stages WHERE connection_id=? AND operation_id=? ORDER BY number`, conn, operation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := []domains.OperationStage{}
	for rows.Next() {
		var stage domains.OperationStage
		var started, updated int64
		if err := rows.Scan(&stage.Number, &stage.Name, &stage.State, &started, &updated); err != nil {
			return nil, err
		}
		stage.StartedAt = stamp(started)
		stage.UpdatedAt = stamp(updated)
		stages = append(stages, stage)
	}
	return stages, rows.Err()
}
