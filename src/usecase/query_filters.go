package usecase

import (
	"context"
	"github.com/mimalef70/goomni/src/domains"
)

func (s *Service) ListOperationsFiltered(ctx context.Context, id string, f domains.OperationFilter) ([]domains.Operation, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = s.validateQueryIdentity(d, f.Peer, ""); err != nil {
		return nil, err
	}
	if err = s.validateOperationFilter(d, f.Operation); err != nil {
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
	if err = s.validateQueryIdentity(d, f.Peer, ""); err != nil {
		return nil, err
	}
	if err = s.validateOperationFilter(d, f.Operation); err != nil {
		return nil, err
	}
	f.Limit, f.Offset = page(f.Limit, f.Offset)
	return s.store.ListSchedulesFiltered(ctx, d.ConnectionID, f)
}

func (s *Service) validateOperationFilter(d domains.Device, operation string) error {
	if operation == "" {
		return nil
	}
	r, err := s.options.Providers.Get(d.Provider)
	if err != nil {
		return err
	}
	def, ok := operationDefinition(r.Contract, operation)
	if !ok || def.Mode != "mutation" {
		return domains.E("INVALID_FILTER", "operation is not a mutation for this provider", 400)
	}
	return nil
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
	if err = s.validateQueryIdentity(d, f.Peer, f.SenderID); err != nil {
		return nil, err
	}
	f.Limit, f.Offset = page(f.Limit, f.Offset)
	return s.store.ListEventsFiltered(ctx, d.ConnectionID, f)
}
