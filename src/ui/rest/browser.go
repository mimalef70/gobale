package rest

import (
	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/goomni/src/config"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/ui/web"
)

func (s *Server) registerBrowserUI(r fiber.Router) error {
	assets := s.opts.UIAssets
	if assets == nil {
		var err error
		assets, err = web.Validate(s.opts.Version, config.ContractSHA256)
		if err != nil {
			return err
		}
	}
	sessions, err := NewAdminSessions(AdminSessionOptions{BasicAuth: s.opts.BasicAuth, BasePath: s.opts.BasePath, PublicOrigin: s.opts.UIPublicOrigin})
	if err != nil {
		return err
	}
	sessions.Register(r)
	api := r.Group("/ui/api", sessions.Protect, func(c fiber.Ctx) error {
		c.Locals("goomni.browser", true)
		return c.Next()
	})
	s.registerAdminRoutes(api)
	api.Use(func(c fiber.Ctx) error { return domains.E("NOT_FOUND", "route not found", 404) })
	r.Get("/ui", sessions.Security, assets.Handler(s.opts.BasePath))
	r.Get("/ui/*", sessions.Security, assets.Handler(s.opts.BasePath))
	// The browser namespace must never fall through to the public API's Basic
	// challenge, including unsupported methods and unknown auth routes.
	missing := func(c fiber.Ctx) error { return domains.E("NOT_FOUND", "route not found", 404) }
	r.All("/ui", sessions.Security, missing)
	r.All("/ui/*", sessions.Security, missing)
	return nil
}
