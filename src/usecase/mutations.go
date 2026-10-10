package usecase

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

// Mutate places a reviewed provider mutation in the same durable, per-account
// journal as sends. Neither an API caller nor a retry may choose its wire ID.
func (s *Service) Mutate(ctx context.Context, id, operation string, payload json.RawMessage, key string) (domains.Operation, error) {
	if strings.TrimSpace(key) == "" || len(key) > 256 {
		return domains.Operation{}, domains.E("IDEMPOTENCY_KEY_REQUIRED", "provide an Idempotency-Key of 1 to 256 bytes for provider mutations", 400)
	}
	device, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Operation{}, err
	}
	normalized, peer, err := s.normalizeMutation(device, operation, payload)
	if err != nil {
		return domains.Operation{}, err
	}
	request := domains.SendRequest{Kind: "operation", Operation: operation, Payload: normalized, Peer: peer}
	// Media-backed mutations use the same account-scoped admission and retention
	// pin as ordinary sends. A queued avatar must survive manual cleanup/restart.
	var media struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(normalized, &media); err == nil {
		request.MediaID = media.MediaID
	}
	op, _, err := s.store.Enqueue(ctx, device.ConnectionID, request, key, s.admissionLimits())
	return op, err
}

func mutationWirePayload(request domains.SendRequest) (json.RawMessage, error) {
	if request.Kind != "operation" || request.Operation == "" || !domains.ValidOpaqueID(request.RequestID) {
		return nil, domains.E("INVALID_OPERATION", "invalid persisted provider mutation", 500)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(request.Payload, &fields); err != nil {
		return nil, err
	}
	fields["request_id"], _ = json.Marshal(request.RequestID)
	return json.Marshal(fields)
}
