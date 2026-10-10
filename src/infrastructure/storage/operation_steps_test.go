package storage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestCompoundStagesAreDurablePrivateAndBoundToClaim(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	a := device(t, s, "stages")
	b := device(t, s, "other")
	op, _, err := s.Enqueue(ctx, a.ConnectionID, textRequest("compound"), "stage-key", AdmissionLimits{})
	require.NoError(t, err)
	stage := domains.OperationStage{Number: 1, Name: "upload", State: "started", Nonce: "synthetic-nonce", Data: json.RawMessage(`{"file_id":"private-file-reference"}`)}
	require.Error(t, s.RecordOperationStage(ctx, a.ConnectionID, op.ID, op.Request.RequestID, stage))
	_, err = s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.Error(t, s.RecordOperationStage(ctx, b.ConnectionID, op.ID, op.Request.RequestID, stage))
	require.Error(t, s.RecordOperationStage(ctx, a.ConnectionID, op.ID, "wrong-request", stage))
	require.NoError(t, s.RecordOperationStage(ctx, a.ConnectionID, op.ID, op.Request.RequestID, stage))
	stage.State = "succeeded"
	require.NoError(t, s.RecordOperationStage(ctx, a.ConnectionID, op.ID, op.Request.RequestID, stage))
	changed := stage
	changed.Nonce = "changed"
	require.Error(t, s.RecordOperationStage(ctx, a.ConnectionID, op.ID, op.Request.RequestID, changed))
	changed = stage
	changed.State = "started"
	require.Error(t, s.RecordOperationStage(ctx, a.ConnectionID, op.ID, op.Request.RequestID, changed))
	stages, err := s.OperationStages(ctx, a.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Len(t, stages, 1)
	public, err := json.Marshal(stages)
	require.NoError(t, err)
	require.NotContains(t, string(public), "private-file-reference")
	require.NotContains(t, string(public), "synthetic-nonce")
	var cipher []byte
	require.NoError(t, s.db.QueryRow(`SELECT private FROM operation_stages WHERE operation_id=?`, op.ID).Scan(&cipher))
	require.NotContains(t, string(cipher), "private-file-reference")
	require.NoError(t, s.Close())
	restored, err := Open(path, testKey)
	require.NoError(t, err)
	defer restored.Close()
	after, err := restored.OperationStages(ctx, a.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, stages, after)
	current, err := restored.GetOperation(ctx, a.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", current.State)
	require.Error(t, restored.RecordOperationStage(ctx, a.ConnectionID, op.ID, op.Request.RequestID, stage))
}
