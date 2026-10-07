package usecase

import (
	"context"
	"github.com/mimalef70/gobale/src/domains"
)

func (s *Service) ListOperationsFiltered(ctx context.Context, id string, f domains.OperationFilter) ([]domains.Operation, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	f.Limit, f.Offset = page(f.Limit, f.Offset)
	return s.store.ListOperationsFiltered(ctx, d.ConnectionID, f)
}
func (s *Service) ListSchedulesFiltered(ctx context.Context, id string, f domains.ScheduleFilter) ([]domains.Schedule, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	f.Limit, f.Offset = page(f.Limit, f.Offset)
	return s.store.ListSchedulesFiltered(ctx, d.ConnectionID, f)
}
func (s *Service) ListScheduleOccurrences(ctx context.Context, id, scheduleID, state string, limit, offset int) ([]domains.ScheduleOccurrence, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.store.ListScheduleOccurrences(ctx, d.ConnectionID, scheduleID, state, limit, offset)
}
func (s *Service) EventsFiltered(ctx context.Context, id string, f domains.EventFilter) ([]domains.Event, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	f.Limit, f.Offset = page(f.Limit, f.Offset)
	return s.store.ListEventsFiltered(ctx, d.ConnectionID, f)
}
