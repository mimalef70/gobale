package rest

import (
	"context"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/goomni/src/domains"
)

// Only the current request is recovered. Background workers keep their own
// durable lifecycle; an HTTP panic never requeues or resolves their work.
func (s *Server) recoverRequest(c fiber.Ctx) (err error) {
	defer func() {
		if recover() != nil {
			s.operational.panics.Add(1)
			c.Response().ResetBody()
			c.Set("Cache-Control", "no-store")
			err = domains.E("INTERNAL_SERVER_ERROR", "operation failed", 500)
		}
	}()
	return c.Next()
}

func (s *Server) requestDeadline(c fiber.Ctx) error {
	if s.streamingRequest(c) {
		return c.Next()
	}
	ctx, cancel := context.WithTimeout(c.Context(), s.opts.RequestTimeout)
	defer cancel()
	c.SetContext(ctx)
	err := c.Next()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.operational.timeouts.Add(1)
		// A send accepted by the outbox returns 202 independently of this
		// request deadline. Do not replace it with an ambiguous generic error.
		if errors.Is(err, context.DeadlineExceeded) {
			return domains.E("REQUEST_TIMEOUT", "request deadline exceeded", 504)
		}
	}
	return err
}

func (s *Server) streamingRequest(c fiber.Ctx) bool {
	// Fiber's default routing is case-insensitive and ignores trailing slashes.
	p := strings.ToLower(strings.TrimRight(c.Path(), "/"))
	base := strings.ToLower(s.opts.BasePath)
	if base != "" {
		if !strings.HasPrefix(p, base+"/") {
			return false
		}
		p = strings.TrimPrefix(p, base)
	}
	if c.Method() == "POST" && p == "/media" {
		return true
	}
	if c.Method() != "GET" && c.Method() != "HEAD" {
		return false
	}
	return strings.HasPrefix(p, "/media/") || p == "/user/avatar" ||
		(strings.HasPrefix(p, "/message/") && strings.HasSuffix(p, "/download"))
}
