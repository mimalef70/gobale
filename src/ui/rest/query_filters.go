package rest

import (
	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/goomni/src/domains"
	"strconv"
	"time"
)

func filterTime(c fiber.Ctx, key string) (*time.Time, error) {
	raw := c.Query(key)
	if raw == "" {
		return nil, nil
	}
	v, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, domains.E("INVALID_FILTER", key+" must be RFC3339", 400)
	}
	return &v, nil
}
func (s *Server) operations(c fiber.Ctx) error {
	d, err := s.device(c)
	if err != nil {
		return err
	}
	l, o := page(c)
	f := domains.OperationFilter{State: c.Query("state"), Kind: c.Query("kind"), Operation: c.Query("operation"), Peer: c.Query("peer"), ScheduleID: c.Query("schedule_id"), Limit: l, Offset: o}
	if f.CreatedAfter, err = filterTime(c, "created_after"); err != nil {
		return err
	}
	if f.CreatedBefore, err = filterTime(c, "created_before"); err != nil {
		return err
	}
	v, err := s.service.ListOperationsFiltered(c.Context(), d.ID, f)
	return result(c, v, err)
}
func (s *Server) schedulesFiltered(c fiber.Ctx) error {
	d, err := s.device(c)
	if err != nil {
		return err
	}
	l, o := page(c)
	f := domains.ScheduleFilter{State: c.Query("state"), Kind: c.Query("kind"), Operation: c.Query("operation"), Peer: c.Query("peer"), Limit: l, Offset: o}
	if f.CreatedAfter, err = filterTime(c, "created_after"); err != nil {
		return err
	}
	if f.CreatedBefore, err = filterTime(c, "created_before"); err != nil {
		return err
	}
	v, err := s.service.ListSchedulesFiltered(c.Context(), d.ID, f)
	return result(c, v, err)
}
func (s *Server) scheduleOccurrences(c fiber.Ctx) error {
	d, err := s.device(c)
	if err != nil {
		return err
	}
	l, o := page(c)
	v, err := s.service.ListScheduleOccurrences(c.Context(), d.ID, c.Params("schedule_id"), c.Query("state"), l, o)
	return result(c, v, err)
}
func (s *Server) events(c fiber.Ctx) error {
	d, err := s.device(c)
	if err != nil {
		return err
	}
	l, o := page(c)
	f := domains.EventFilter{Peer: c.Query("peer"), Search: c.Query("search"), Event: c.Query("event"), Direction: c.Query("direction"), SenderID: c.Query("sender_id"), Limit: l, Offset: o}
	if p := c.Params("chat_jid"); p != "" {
		if f.Peer != "" && f.Peer != p {
			return domains.E("INVALID_FILTER", "conflicting peer filters", 400)
		}
		f.Peer = p
	}
	if f.StartTime, err = filterTime(c, "start_time"); err != nil {
		return err
	}
	if f.EndTime, err = filterTime(c, "end_time"); err != nil {
		return err
	}
	if raw := c.Query("media_only"); raw != "" {
		f.MediaOnly, err = strconv.ParseBool(raw)
		if err != nil {
			return domains.E("INVALID_FILTER", "media_only must be boolean", 400)
		}
	}
	v, err := s.service.EventsFiltered(c.Context(), d.ID, f)
	return result(c, v, err)
}
