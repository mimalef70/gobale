package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/validations"
)

// SendUpload accepts a new owned local asset and enqueues its send atomically.
// Provider contact remains exclusively in the durable outbox worker.
func (s *Service) SendUpload(ctx context.Context, id string, request domains.SendRequest, key string, media domains.Media, digest string) (domains.Operation, error) {
	if request.Operation != "" || len(request.Payload) > 0 || request.RequestID != "" || request.MediaID != media.ID {
		return domains.Operation{}, domains.E("INVALID_REQUEST", "multipart sends require a new file and gateway-assigned request identity", 400)
	}
	switch request.Kind {
	case "file", "image", "audio", "video", "voice":
	default:
		return domains.Operation{}, domains.E("INVALID_REQUEST", "multipart sends require a media endpoint", 400)
	}
	if request.IsScheduled() {
		return domains.Operation{}, domains.E("USE_SCHEDULE_ENDPOINT", "upload media separately and use the schedule endpoint for delayed sends", 400)
	}
	if err := request.Validate(); err != nil {
		return domains.Operation{}, err
	}
	if strings.TrimSpace(key) == "" || len(key) > 256 {
		return domains.Operation{}, domains.E("INVALID_IDEMPOTENCY_KEY", "a nonempty idempotency key of at most 256 bytes is required", 400)
	}
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Operation{}, err
	}
	if err = s.validateSend(d, request); err != nil {
		return domains.Operation{}, err
	}
	if d.ConnectionID != media.ConnectionID {
		return domains.Operation{}, domains.E("DEVICE_SCOPE_MISMATCH", "upload belongs to another connection", 409)
	}
	return s.store.EnqueueUpload(ctx, d.ConnectionID, request, key, media, digest, s.admissionLimits())
}

// ScheduleUpload accepts the same bounded file as an immediate send, but keeps
// it pinned by a durable schedule. No provider upload happens during admission.
func (s *Service) ScheduleUpload(ctx context.Context, id string, request domains.SendRequest, key string, media domains.Media, digest string) (domains.Schedule, error) {
	if request.Operation != "" || len(request.Payload) > 0 || request.RequestID != "" || request.MediaID != media.ID {
		return domains.Schedule{}, domains.E("INVALID_REQUEST", "multipart schedules require gateway-assigned media and request identities", 400)
	}
	switch request.Kind {
	case "file", "image", "audio", "video", "voice":
	default:
		return domains.Schedule{}, domains.E("INVALID_REQUEST", "multipart schedules require a media kind", 400)
	}
	if err := request.Validate(); err != nil {
		return domains.Schedule{}, err
	}
	if !request.IsScheduled() || strings.TrimSpace(key) == "" || len(key) > 256 {
		return domains.Schedule{}, domains.E("INVALID_REQUEST", "scheduled_at and a nonempty idempotency key of at most 256 bytes are required", 400)
	}
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Schedule{}, err
	}
	if err = s.validateSend(d, request); err != nil {
		return domains.Schedule{}, err
	}
	if d.ConnectionID != media.ConnectionID {
		return domains.Schedule{}, domains.E("DEVICE_SCOPE_MISMATCH", "upload belongs to another connection", 409)
	}
	previous, found, err := s.store.LookupScheduleUpload(ctx, d.ConnectionID, request, key, media, digest)
	if err != nil || found {
		return previous, err
	}
	if strings.EqualFold(strings.TrimSpace(request.Recurrence), "once") {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", "use recurrence none for a one-time send", 400)
	}
	spec, err := validations.ParseScheduleOptions(request.ScheduleOptions, time.Now().UTC())
	if err != nil {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", err.Error(), 400)
	}
	return s.store.CreateScheduleUpload(ctx, d.ConnectionID, request, spec.ScheduledAt, key, media, digest)
}
