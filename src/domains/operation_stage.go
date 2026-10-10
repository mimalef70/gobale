package domains

import (
	"context"
	"encoding/json"
	"time"
)

// OperationStage exposes compound progress without exposing upload capabilities,
// tokens or request nonces. Private details are encrypted by the storage owner.
type OperationStage struct {
	Number    int             `json:"number"`
	Name      string          `json:"name"`
	State     string          `json:"state"`
	Nonce     string          `json:"-"`
	Data      json.RawMessage `json:"-"`
	StartedAt time.Time       `json:"started_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}
type OperationStageRecorder func(context.Context, OperationStage) error
type operationStageKey struct{}

func WithOperationStageRecorder(ctx context.Context, record OperationStageRecorder) context.Context {
	return context.WithValue(ctx, operationStageKey{}, record)
}
func RecordOperationStage(ctx context.Context, stage OperationStage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record, ok := ctx.Value(operationStageKey{}).(OperationStageRecorder); ok {
		return record(ctx, stage)
	}
	return nil // Standalone protocol clients have no gateway journal.
}
