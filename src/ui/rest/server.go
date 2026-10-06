// Package rest exposes the GoBale HTTP contract through application usecases.
package rest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	pkgError "github.com/mimalef70/gobale/src/pkg/error"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/mimalef70/gobale/src/pkg/utils"
	"github.com/mimalef70/gobale/src/usecase"
)

type Options struct {
	MediaSlots                              chan struct{}
	BasicAuth, BasePath, Version, MediaRoot string
	MaxMediaBytes                           int64
	SendWait                                time.Duration
}
type Server struct {
	App        *fiber.App
	service    *usecase.Service
	store      *storage.Store
	opts       Options
	mediaSlots chan struct{}
}

func New(service *usecase.Service, store *storage.Store, opts Options) (*Server, error) {
	if !strings.Contains(opts.BasicAuth, ":") || strings.HasSuffix(opts.BasicAuth, ":") || strings.HasPrefix(opts.BasicAuth, ":") {
		return nil, fmt.Errorf("APP_BASIC_AUTH must contain a nonempty username:password")
	}
	if opts.BasePath != "" && (!strings.HasPrefix(opts.BasePath, "/") || strings.ContainsAny(opts.BasePath, "*?:#") || strings.Contains(opts.BasePath, "..")) {
		return nil, fmt.Errorf("invalid base path")
	}
	opts.BasePath = strings.TrimRight(opts.BasePath, "/")
	if opts.MaxMediaBytes <= 0 {
		opts.MaxMediaBytes = 64 << 20
	}
	if opts.SendWait <= 0 {
		opts.SendWait = 40 * time.Second
	}
	slots := opts.MediaSlots
	if slots == nil {
		slots = make(chan struct{}, 4)
	}
	s := &Server{service: service, store: store, opts: opts, mediaSlots: slots}
	s.App = fiber.New(fiber.Config{AppName: "GoBale", BodyLimit: int(opts.MaxMediaBytes + 4096), StreamRequestBody: true, DisablePreParseMultipartForm: true, ReadTimeout: 60 * time.Second, WriteTimeout: 60 * time.Second, ErrorHandler: s.handleError})
	r := s.App.Group(opts.BasePath)
	r.Get("/health", func(c fiber.Ctx) error { return success(c, map[string]any{"status": "ok"}) })
	expected := sha256.Sum256([]byte("Basic " + base64.StdEncoding.EncodeToString([]byte(opts.BasicAuth))))
	r.Use(func(c fiber.Ctx) error {
		// Authenticated results can contain messages or short-lived Mini App
		// launch credentials. Intermediaries must not cache these responses.
		c.Set("Cache-Control", "no-store")
		c.Set("X-Content-Type-Options", "nosniff")
		got := sha256.Sum256([]byte(c.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], expected[:]) != 1 {
			c.Set("WWW-Authenticate", `Basic realm="GoBale"`)
			return domains.E("UNAUTHORIZED", "authentication required", 401)
		}
		return c.Next()
	})
	r.Get("/ready", func(c fiber.Ctx) error {
		if err := store.Ping(c.Context()); err != nil {
			return domains.E("NOT_READY", "storage unavailable", 503)
		}
		return success(c, map[string]any{"status": "ready"})
	})
	r.Get("/app/info", s.info)
	r.Get("/app/capabilities", func(c fiber.Ctx) error { return success(c, domains.OperationDefinitions()) })
	r.Post("/operations/:operation", func(c fiber.Ctx) error {
		if _, ok := domains.OperationDefinition(c.Params("operation")); !ok {
			return domains.Unsupported(c.Params("operation"))
		}
		return s.provider(c.Params("operation"))(c)
	})
	r.Get("/devices", s.devices)
	r.Get("/app/devices", s.devices)
	r.Post("/devices", s.createDevice)
	r.Get("/devices/:device_id", func(c fiber.Ctx) error {
		d, e := service.GetDevice(c.Context(), c.Params("device_id"))
		return result(c, d, e)
	})
	r.Delete("/devices/:device_id", func(c fiber.Ctx) error {
		return result(c, nil, service.DeleteDevice(c.Context(), c.Params("device_id")))
	})
	r.Get("/devices/:device_id/status", func(c fiber.Ctx) error {
		v, e := service.Status(c.Context(), c.Params("device_id"))
		return result(c, v, e)
	})
	r.Post("/devices/:device_id/reconnect", func(c fiber.Ctx) error { return result(c, nil, service.Reconnect(c.Context(), c.Params("device_id"))) })
	r.Post("/devices/:device_id/logout", func(c fiber.Ctx) error { return result(c, nil, service.Logout(c.Context(), c.Params("device_id"))) })
	r.Post("/devices/:device_id/login", s.login)
	r.Post("/devices/:device_id/login/code", s.code)
	r.Post("/devices/:device_id/login/password", s.password)
	r.Get("/devices/:device_id/webhook", func(c fiber.Ctx) error {
		v, e := service.GetWebhook(c.Context(), c.Params("device_id"))
		return result(c, v, e)
	})
	r.Patch("/devices/:device_id/webhook", s.patchWebhook)
	r.Get("/app/status", s.status)
	r.Get("/send/operations/:send_id", s.operation)
	r.Get("/send/schedules", s.schedules)
	r.Post("/send/schedules", s.schedule)
	r.Get("/send/schedules/:schedule_id", s.getSchedule)
	for _, action := range []string{"pause", "resume", "cancel"} {
		r.Post("/send/schedules/:schedule_id/"+action, s.scheduleAction(action))
	}
	for _, kind := range []string{"message", "image", "file", "audio", "video", "voice"} {
		r.Post("/send/"+kind, s.send(kind))
	}
	r.Get("/deliveries", s.deliveries)
	r.Get("/deliveries/:delivery_id", s.delivery)
	r.Post("/deliveries/:delivery_id/retry", s.retryDelivery)
	r.Post("/deliveries/:delivery_id/replay", s.replayDelivery)
	r.Get("/chats", s.chats)
	r.Get("/chat/:chat_jid/messages", s.messages)
	r.Get("/chat/:chat_jid/history", s.provider("chat.history"))
	r.Post("/media", s.upload)
	r.Post("/media/fetch", s.fetchMedia)
	r.Get("/media/:media_id", s.download)
	r.Get("/message/:message_id/download", s.downloadMessage)
	r.Get("/user/avatar", s.avatar)
	r.Get("/metrics", s.metrics)
	allProviderRoutes := append([]ProviderRoute(nil), providerRoutes...)
	for _, contract := range domains.OperationDefinitions() {
		found := false
		for _, route := range allProviderRoutes {
			if route.Method == contract.Method && route.Path == contract.Path {
				found = true
				break
			}
		}
		if !found {
			allProviderRoutes = append(allProviderRoutes, ProviderRoute{contract.Method, contract.Path, contract.Operation})
		}
	}
	for _, route := range allProviderRoutes {
		switch route.Method {
		case "GET":
			r.Get(route.Path, s.provider(route.Operation))
		case "POST":
			r.Post(route.Path, s.provider(route.Operation))
		case "PUT":
			r.Put(route.Path, s.provider(route.Operation))
		case "DELETE":
			r.Delete(route.Path, s.provider(route.Operation))
		case "PATCH":
			r.Patch(route.Path, s.provider(route.Operation))
		}
	}
	r.Use(func(c fiber.Ctx) error { return domains.E("NOT_FOUND", "route not found", 404) })
	return s, nil
}
func success(c fiber.Ctx, v any) error {
	return c.JSON(utils.ResponseData{Code: "SUCCESS", Message: "Success", Results: v})
}
func result(c fiber.Ctx, v any, e error) error {
	if e != nil {
		return e
	}
	return success(c, v)
}
func (s *Server) handleError(c fiber.Ctx, err error) error {
	var de *domains.Error
	if errors.As(err, &de) {
		status := de.HTTP
		if status < 400 || status > 599 {
			status = 500
		}
		return c.Status(status).JSON(utils.ResponseData{Code: de.Code, Message: de.Message})
	}
	var ge pkgError.GenericError
	if errors.As(err, &ge) {
		return c.Status(ge.StatusCode()).JSON(utils.ResponseData{Code: ge.ErrCode(), Message: ge.Error()})
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return c.Status(fe.Code).JSON(utils.ResponseData{Code: "INVALID_REQUEST", Message: http.StatusText(fe.Code)})
	}
	return c.Status(500).JSON(utils.ResponseData{Code: "INTERNAL_SERVER_ERROR", Message: "operation failed"})
}
func decode(c fiber.Ctx, v any) error {
	var reader io.Reader = c.Request().BodyStream()
	if reader == nil {
		reader = bytes.NewReader(c.Body())
	}
	body, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return domains.E("INVALID_REQUEST", "incomplete JSON body", 400)
	}
	if len(body) > 1<<20 {
		return domains.E("INVALID_REQUEST", "JSON body exceeds 1 MiB", 413)
	}
	if err = domains.ValidateJSONObject(body); err != nil {
		return err
	}
	if err = json.Unmarshal(body, v); err != nil {
		return domains.E("INVALID_REQUEST", "invalid JSON body", 400)
	}
	return nil
}
func (s *Server) device(c fiber.Ctx) (domains.Device, error) {
	id := c.Get("X-Device-Id")
	if id == "" {
		id = c.Query("device_id")
	}
	return s.service.ResolveDevice(c.Context(), id)
}
func page(c fiber.Ctx) (int, int) {
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
func (s *Server) devices(c fiber.Ctx) error {
	v, e := s.service.ListDevices(c.Context())
	return result(c, v, e)
}
func (s *Server) createDevice(c fiber.Ctx) error {
	var req struct {
		ID     string    `json:"device_id"`
		URL    *string   `json:"webhook_url"`
		Secret *string   `json:"webhook_secret"`
		Events *[]string `json:"webhook_events"`
	}
	if e := decode(c, &req); e != nil {
		return e
	}
	v, e := s.service.CreateDevice(c.Context(), req.ID)
	if e != nil {
		return e
	}
	if req.URL != nil || req.Secret != nil || req.Events != nil {
		v.Webhook, e = s.service.PatchWebhook(c.Context(), v.ID, domains.WebhookPatch{URL: req.URL, Secret: req.Secret, Events: req.Events})
		if e != nil {
			_ = s.service.DeleteDevice(c.Context(), v.ID)
			return e
		}
	}
	return c.Status(201).JSON(utils.ResponseData{Code: "SUCCESS", Message: "Device created", Results: v})
}
func (s *Server) login(c fiber.Ctx) error {
	var req struct {
		Phone string `json:"phone"`
	}
	if e := decode(c, &req); e != nil {
		return e
	}
	v, e := s.service.StartAuth(c.Context(), c.Params("device_id"), req.Phone)
	return result(c, v, e)
}
func (s *Server) code(c fiber.Ctx) error {
	var req struct {
		ID   string `json:"challenge_id"`
		Code string `json:"code"`
	}
	if e := decode(c, &req); e != nil {
		return e
	}
	v, e := s.service.SubmitCode(c.Context(), c.Params("device_id"), req.ID, req.Code)
	return result(c, v, e)
}
func (s *Server) password(c fiber.Ctx) error {
	var req struct {
		ID       string `json:"challenge_id"`
		Password string `json:"password"`
	}
	if e := decode(c, &req); e != nil {
		return e
	}
	v, e := s.service.SubmitPassword(c.Context(), c.Params("device_id"), req.ID, req.Password)
	return result(c, v, e)
}
func (s *Server) patchWebhook(c fiber.Ctx) error {
	var req domains.WebhookPatch
	if e := decode(c, &req); e != nil {
		return e
	}
	v, e := s.service.PatchWebhook(c.Context(), c.Params("device_id"), req)
	return result(c, v, e)
}
func (s *Server) status(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	v, e := s.service.Status(c.Context(), d.ID)
	return result(c, v, e)
}
func (s *Server) send(kind string) fiber.Handler {
	return func(c fiber.Ctx) error {
		d, e := s.device(c)
		if e != nil {
			return e
		}
		var req domains.SendRequest
		if e = decode(c, &req); e != nil {
			return e
		}
		req.Kind = kind
		if kind == "message" {
			req.Kind = "text"
		}
		req.RequestID = ""
		if req.IsScheduled() {
			v, e := s.service.CreateScheduleIdempotent(c.Context(), d.ID, req, c.Get("Idempotency-Key"))
			return result(c, v, e)
		}
		op, e := s.service.Send(c.Context(), d.ID, req, c.Get("Idempotency-Key"))
		if e != nil {
			return e
		}
		return s.awaitOperation(c, d.ID, op)
	}
}
func (s *Server) awaitOperation(c fiber.Ctx, deviceID string, op domains.Operation) error {
	var e error
	ctx, cancel := context.WithTimeout(c.Context(), s.opts.SendWait)
	defer cancel()
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for op.State == "queued" || op.State == "sending" {
		select {
		case <-ctx.Done():
			return c.Status(202).JSON(utils.ResponseData{Code: "ACCEPTED", Message: "Send accepted; inspect send_id for outcome", Results: op})
		case <-timer.C:
			op, e = s.service.GetOperation(c.Context(), deviceID, op.ID)
			if e != nil {
				return e
			}
		}
	}
	if op.State == "unknown" {
		return c.Status(202).JSON(utils.ResponseData{Code: "SEND_UNKNOWN", Message: "Provider outcome is unknown; do not blindly resend", Results: op})
	}
	if op.State != "succeeded" {
		status := 422
		if op.ErrorCode == "FEATURE_NOT_SUPPORTED" {
			status = 501
		}
		if op.ErrorCode == "AUTH_REQUIRED" {
			status = 409
		}
		return c.Status(status).JSON(utils.ResponseData{Code: op.ErrorCode, Message: op.ErrorMessage, Results: op})
	}
	return success(c, op)
}

func (s *Server) operation(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	v, e := s.service.GetOperation(c.Context(), d.ID, c.Params("send_id"))
	return result(c, v, e)
}
func (s *Server) schedules(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	l, o := page(c)
	v, e := s.service.ListSchedules(c.Context(), d.ID, l, o)
	return result(c, v, e)
}
func (s *Server) schedule(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	var req domains.SendRequest
	if e = decode(c, &req); e != nil {
		return e
	}
	req.RequestID = ""
	v, e := s.service.CreateScheduleIdempotent(c.Context(), d.ID, req, c.Get("Idempotency-Key"))
	return result(c, v, e)
}
func (s *Server) getSchedule(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	v, e := s.service.GetSchedule(c.Context(), d.ID, c.Params("schedule_id"))
	return result(c, v, e)
}
func (s *Server) scheduleAction(action string) fiber.Handler {
	return func(c fiber.Ctx) error {
		d, e := s.device(c)
		if e != nil {
			return e
		}
		return result(c, nil, s.service.ScheduleAction(c.Context(), d.ID, c.Params("schedule_id"), action))
	}
}
func (s *Server) deliveries(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	l, o := page(c)
	v, e := s.service.ListDeliveries(c.Context(), d.ID, l, o)
	return result(c, v, e)
}
func (s *Server) delivery(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	v, e := s.service.GetDelivery(c.Context(), d.ID, c.Params("delivery_id"))
	return result(c, v, e)
}
func (s *Server) retryDelivery(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	return result(c, nil, s.service.RetryDelivery(c.Context(), d.ID, c.Params("delivery_id")))
}
func (s *Server) replayDelivery(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	v, e := s.service.ReplayDelivery(c.Context(), d.ID, c.Params("delivery_id"))
	return result(c, v, e)
}
func (s *Server) chats(c fiber.Ctx) error {
	if c.Query("source") == "remote" {
		return s.provider("chat.list")(c)
	}
	d, e := s.device(c)
	if e != nil {
		return e
	}
	l, o := page(c)
	v, e := s.service.Chats(c.Context(), d.ID, l, o)
	return result(c, v, e)
}
func (s *Server) messages(c fiber.Ctx) error {
	d, e := s.device(c)
	if e != nil {
		return e
	}
	l, o := page(c)
	v, e := s.service.Messages(c.Context(), d.ID, c.Params("chat_jid"), l, o)
	return result(c, v, e)
}
func (s *Server) provider(operation string) fiber.Handler {
	return func(c fiber.Ctx) error {
		d, e := s.device(c)
		if e != nil {
			return e
		}
		payload := map[string]any{}
		if c.Request().Header.ContentLength() > 0 || c.Request().BodyStream() != nil {
			if e = decode(c, &payload); e != nil {
				return e
			}
			if payload == nil {
				return domains.E("INVALID_REQUEST", "provider request body must be a JSON object", 400)
			}
		}
		for k, v := range c.Queries() {
			if k != "device_id" {
				if _, ok := payload[k]; !ok {
					if contract, ok := domains.OperationDefinition(operation); ok {
						field, exists := contract.Request.Properties[k]
						if !exists {
							return domains.E("INVALID_REQUEST", "unsupported query field", 400)
						}
						value, err := operationQueryValue(field, v)
						if err != nil {
							return err
						}
						payload[k] = value
						continue
					}
					if k == "peer" {
						parts := strings.SplitN(v, ":", 2)
						if len(parts) != 2 {
							return domains.E("INVALID_PEER", "peer query must be type:id", 400)
						}
						peer := domains.Peer{Type: parts[0], ID: parts[1]}
						if err := peer.Validate(); err != nil {
							return err
						}
						payload[k] = peer
					} else if k == "is_owner" {
						b, err := strconv.ParseBool(v)
						if err != nil {
							return domains.E("INVALID_REQUEST", "is_owner must be a boolean", 400)
						}
						payload[k] = b
					} else if k == "limit" || k == "load_mode" {
						n, err := strconv.Atoi(v)
						if err != nil {
							return domains.E("INVALID_REQUEST", k+" must be an integer", 400)
						}
						payload[k] = n
					} else {
						payload[k] = v
					}
				}
			}
		}
		if id := c.Params("message_id"); id != "" {
			payload["message_id"] = id
		}
		if id := c.Params("chat_jid"); id != "" {
			parts := strings.SplitN(id, ":", 2)
			if len(parts) != 2 {
				return domains.E("INVALID_PEER", "chat identifier must be type:id", 400)
			}
			payload["peer"] = domains.Peer{Type: parts[0], ID: parts[1]}
		}
		raw, _ := json.Marshal(payload)
		if domains.IsExtendedMutation(operation) {
			op, err := s.service.Mutate(c.Context(), d.ID, operation, raw, c.Get("Idempotency-Key"))
			if err != nil {
				return err
			}
			return s.awaitOperation(c, d.ID, op)
		}
		switch operation {
		case "group.create", "group.invite", "group.title", "group.description", "group.remove", "message.edit", "message.read", "message.forward", "message.delete":
			op, err := s.service.Mutate(c.Context(), d.ID, operation, raw, c.Get("Idempotency-Key"))
			if err != nil {
				return err
			}
			return s.awaitOperation(c, d.ID, op)
		}
		v, e := s.service.Call(c.Context(), d.ID, operation, raw)
		return result(c, v, e)
	}
}

func operationQueryValue(field domains.FieldSchema, value string) (any, error) {
	invalid := func() (any, error) {
		return nil, domains.E("INVALID_REQUEST", "query value does not match the operation schema", 400)
	}
	switch field.Type {
	case "string":
		return value, nil
	case "integer":
		n, e := strconv.ParseInt(value, 10, 64)
		if e != nil {
			return invalid()
		}
		return n, nil
	case "boolean":
		b, e := strconv.ParseBool(value)
		if e != nil {
			return invalid()
		}
		return b, nil
	case "object":
		if _, isPeer := field.Properties["id"]; isPeer {
			if parts := strings.SplitN(value, ":", 2); len(parts) == 2 {
				return domains.Peer{Type: parts[0], ID: parts[1]}, nil
			}
		}
		fallthrough
	case "array":
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		if decoder.Decode(&decoded) != nil {
			return invalid()
		}
		return decoded, nil
	default:
		return invalid()
	}
}
func (s *Server) info(c fiber.Ctx) error {
	return success(c, map[string]any{"name": "GoBale", "version": s.opts.Version, "release_stage": "experimental", "provider": "bale", "capabilities": map[string]any{"multi_device": true, "per_device_webhook": true, "durable_outbox": true, "scheduled_sends": true, "short_restart_recovery_verified": true, "live_accounts_tested": 2}, "protocol_note": "Native implementation; provider capability verification is documented separately"})
}
func (s *Server) metrics(c fiber.Ctx) error {
	stats, err := s.store.Stats(c.Context())
	if err != nil {
		return err
	}
	var b strings.Builder
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "# TYPE gobale_%s gauge\ngobale_%s %d\n", k, k, stats[k])
	}
	m := s.service.WorkerMetrics()
	fmt.Fprintf(&b, "# TYPE gobale_send_attempts_total counter\ngobale_send_attempts_total %d\ngobale_send_unknown_total %d\ngobale_webhook_attempts_total %d\ngobale_webhook_failures_total %d\ngobale_worker_errors_total %d\n", m.SendAttempts, m.SendUnknown, m.WebhookAttempts, m.WebhookFailures, m.WorkerErrors)
	for _, h := range []struct {
		name    string
		buckets map[string]uint64
		sum     float64
		count   uint64
	}{{"send_duration_seconds", m.SendDurationBuckets, m.SendDurationSeconds, m.SendAttempts}, {"webhook_duration_seconds", m.WebhookDurationBuckets, m.WebhookDurationSeconds, m.WebhookAttempts}} {
		fmt.Fprintf(&b, "# TYPE gobale_%s histogram\n", h.name)
		for _, bound := range []string{"0.01", "0.1", "1", "5", "10", "40", "+Inf"} {
			fmt.Fprintf(&b, "gobale_%s_bucket{le=%q} %d\n", h.name, bound, h.buckets[bound])
		}
		fmt.Fprintf(&b, "gobale_%s_sum %g\ngobale_%s_count %d\n", h.name, h.sum, h.name, h.count)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(&b, "gobale_go_goroutines %d\ngobale_go_heap_bytes %d\n", runtime.NumGoroutine(), mem.HeapAlloc)
	c.Set("Content-Type", "text/plain; version=0.0.4")
	return c.SendString(b.String())
}

type ProviderRoute struct{ Method, Path, Operation string }

var providerRoutes = []ProviderRoute{
	{"GET", "/user/search", "contacts.search"}, {"GET", "/user/info", "account.info"}, {"GET", "/user/my/contacts", "contacts.list"}, {"GET", "/user/my/groups", "group.list"}, {"GET", "/user/check", "contacts.resolve"}, {"POST", "/user/pushname", "account.name"},
	{"POST", "/message/:message_id/read", "message.read"}, {"POST", "/message/:message_id/update", "message.edit"}, {"POST", "/message/:message_id/delete", "message.delete"}, {"POST", "/message/:message_id/revoke", "message.revoke"}, {"POST", "/message/:message_id/forward", "message.forward"}, {"POST", "/message/:message_id/reaction", "message.reaction.set"},
	{"GET", "/group/participants", "group.members"}, {"GET", "/group/info", "group.info"}, {"POST", "/group", "group.create"}, {"POST", "/group/participants", "group.invite"}, {"POST", "/group/participants/remove", "group.remove"}, {"POST", "/group/participants/promote", "group.promote"}, {"POST", "/group/participants/demote", "group.demote"}, {"POST", "/group/join-with-link", "group.join"}, {"POST", "/group/leave", "group.leave"}, {"POST", "/group/name", "group.title"}, {"POST", "/group/topic", "group.description"}, {"POST", "/group/photo", "group.photo"}, {"GET", "/group/invite-link", "group.link"},
	{"POST", "/send/sticker", "send.sticker"}, {"POST", "/send/poll", "send.poll"}, {"POST", "/send/contact", "send.contact"}, {"POST", "/send/location", "send.location"}, {"POST", "/send/presence", "presence.online"}, {"POST", "/send/chat-presence", "presence.typing"},
}
