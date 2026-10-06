package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/validations"
)

func (s *Service) CreateSchedule(ctx context.Context, id string, request domains.SendRequest) (domains.Schedule, error) {
	return s.CreateScheduleIdempotent(ctx, id, request, "")
}

func (s *Service) CreateScheduleIdempotent(ctx context.Context, id string, request domains.SendRequest, key string) (domains.Schedule, error) {
	if len(key) > 256 {
		return domains.Schedule{}, domains.E("INVALID_IDEMPOTENCY_KEY", "idempotency key exceeds 256 bytes", 400)
	}
	if request.Operation != "" || len(request.Payload) > 0 || request.Kind == "operation" {
		// Only reviewed message producers may recur. Administrative operations,
		// stories and financial actions are never admitted by this scheduler.
		switch request.Operation {
		case "send.poll", "send.sticker", "send.contact", "send.location", "send.template":
		default:
			return domains.Schedule{}, domains.E("INVALID_REQUEST", "this operation cannot be scheduled", 400)
		}
		if (request.Kind != "" && request.Kind != "operation") || request.Phone != "" || request.Text != "" || request.ReplyMessageID != "" || request.MediaID != "" {
			return domains.Schedule{}, domains.E("INVALID_REQUEST", "scheduled operations take message fields inside payload", 400)
		}
		normalized, peer, err := normalizeMutation(request.Operation, request.Payload)
		if err != nil {
			return domains.Schedule{}, err
		}
		if (request.Peer.ID != "" || request.Peer.Type != "") && request.Peer != peer {
			return domains.Schedule{}, domains.E("INVALID_REQUEST", "conflicting scheduled operation destinations", 400)
		}
		request.Kind, request.Peer, request.Payload = "operation", peer, normalized
	} else if err := request.Validate(); err != nil {
		return domains.Schedule{}, err
	}
	if !request.IsScheduled() {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", "scheduled_at is required", 400)
	}
	if request.RequestID != "" {
		return domains.Schedule{}, domains.E("INVALID_REQUEST", "request_id is assigned by the gateway", 400)
	}
	if request.Kind == "" {
		request.Kind = "text"
	}
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Schedule{}, err
	}
	if key != "" {
		// A retry can arrive after scheduled_at or completion. Existing matching
		// work wins before new-schedule time and media availability checks.
		previous, found, err := s.store.LookupScheduleIdempotent(ctx, d.ConnectionID, request, key)
		if err != nil || found {
			return previous, err
		}
	}
	spec, err := validations.ParseScheduleOptions(request.ScheduleOptions, time.Now().UTC())
	if err != nil {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", err.Error(), 400)
	}
	if request.MediaID != "" {
		if _, err = s.store.GetMedia(ctx, d.ConnectionID, request.MediaID); err != nil {
			return domains.Schedule{}, err
		}
	}
	return s.store.CreateScheduleIdempotent(ctx, d.ConnectionID, request, spec.ScheduledAt, key)
}
func (s *Service) ListSchedules(ctx context.Context, id string, limit, offset int) ([]domains.Schedule, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.store.ListSchedules(ctx, d.ConnectionID, limit, offset)
}
func (s *Service) GetSchedule(ctx context.Context, id, scheduleID string) (domains.Schedule, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Schedule{}, err
	}
	return s.store.GetSchedule(ctx, d.ConnectionID, scheduleID)
}
func (s *Service) ScheduleAction(ctx context.Context, id, scheduleID, action string) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	job, err := s.store.GetSchedule(ctx, d.ConnectionID, scheduleID)
	if err != nil {
		return err
	}
	var target string
	switch strings.ToLower(action) {
	case "pause":
		if job.State != "active" {
			return domains.E("INVALID_SCHEDULE_STATE", "only active schedules can be paused", 409)
		}
		target = "paused"
	case "resume":
		if job.State != "paused" {
			return domains.E("INVALID_SCHEDULE_STATE", "only paused schedules can be resumed", 409)
		}
		target = "active"
	case "cancel":
		if job.State != "active" && job.State != "paused" {
			return domains.E("INVALID_SCHEDULE_STATE", "schedule is already terminal", 409)
		}
		target = "cancelled"
	default:
		return domains.E("INVALID_SCHEDULE_ACTION", "action must be pause, resume or cancel", 400)
	}
	return s.store.SetScheduleState(ctx, d.ConnectionID, scheduleID, target)
}
func (s *Service) scheduleLoop() {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		jobs, err := s.store.DueSchedules(s.ctx, time.Now().UTC(), 100)
		s.workerError(err)
		if err == nil {
			for _, job := range jobs {
				if s.ctx.Err() != nil {
					return
				}
				s.materializeSchedule(job)
			}
		}
		if !s.wait() {
			return
		}
	}
}
func (s *Service) materializeSchedule(job domains.Schedule) {
	// Match creation-time parsing, including schedules already persisted with
	// surrounding whitespace. Keep the stored request unchanged: its original
	// representation is part of the existing idempotency contract.
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(job.Request.ScheduledAt))
	if err != nil {
		_ = s.store.SetScheduleState(s.ctx, job.ConnectionID, job.ID, "failed")
		return
	}
	// Validation uses the original creation-time boundary; later occurrences can
	// legitimately be processed after the original scheduled_at.
	spec, err := validations.ParseScheduleOptions(job.Request.ScheduleOptions, start.Add(-time.Nanosecond))
	if err != nil {
		_ = s.store.SetScheduleState(s.ctx, job.ConnectionID, job.ID, "failed")
		return
	}
	now := time.Now().UTC()
	if (spec.EndAt != nil && now.After(*spec.EndAt)) || (spec.OccurrenceLimit > 0 && job.Count >= spec.OccurrenceLimit) {
		_ = s.store.SetScheduleState(s.ctx, job.ConnectionID, job.ID, "completed")
		return
	}
	var next *time.Time
	if spec.OccurrenceLimit == 0 || job.Count+1 < spec.OccurrenceLimit {
		after := job.NextAt
		if now.After(after) {
			after = now
		}
		if candidate, ok := validations.NextScheduleOccurrence(spec, after); ok {
			next = &candidate
		}
	}
	// Storage atomically enqueues an occurrence with a stable idempotency key and
	// advances this schedule. A restart between these actions cannot duplicate it.
	_, err = s.store.MaterializeSchedule(s.ctx, job.ConnectionID, job.ID, job.NextAt, next, s.options.QueueLimit)
	s.workerError(err)
}
func eventTypeFromBody(body json.RawMessage) string {
	var event struct {
		Type string `json:"event"`
	}
	_ = json.Unmarshal(body, &event)
	return event.Type
}
