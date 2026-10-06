package usecase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/mimalef70/gobale/src/domains"
)

func (s *Service) Send(ctx context.Context, id string, request domains.SendRequest, key string) (domains.Operation, error) {
	if request.Operation != "" || len(request.Payload) > 0 || request.Kind == "operation" {
		return domains.Operation{}, domains.E("INVALID_REQUEST", "use the durable mutation endpoint for provider operations", 400)
	}
	if err := request.Validate(); err != nil {
		return domains.Operation{}, err
	}
	if request.IsScheduled() {
		return domains.Operation{}, domains.E("USE_SCHEDULE_ENDPOINT", "use the schedule endpoint for delayed sends", 400)
	}
	if request.RequestID != "" {
		return domains.Operation{}, domains.E("INVALID_REQUEST", "request_id is assigned by the gateway", 400)
	}
	if len(key) > 256 {
		return domains.Operation{}, domains.E("INVALID_IDEMPOTENCY_KEY", "idempotency key exceeds 256 bytes", 400)
	}
	if request.Kind == "" {
		request.Kind = "text"
	}
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Operation{}, err
	}
	// Storage resolves idempotency before checking media, so a completed send
	// can still be inspected through its key after an old asset is retired.
	operation, _, err := s.store.Enqueue(ctx, d.ConnectionID, request, key, s.options.QueueLimit)
	return operation, err
}
func (s *Service) GetOperation(ctx context.Context, id, operationID string) (domains.Operation, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Operation{}, err
	}
	return s.store.GetOperation(ctx, d.ConnectionID, operationID)
}

func (s *Service) sendLoop() {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		jobs, err := s.store.ClaimOperations(s.ctx, 1)
		s.workerError(err)
		if err == nil && len(jobs) > 0 {
			s.processOperation(jobs[0])
			continue
		}
		if !s.wait() {
			return
		}
	}
}
func (s *Service) processOperation(op domains.Operation) {
	// Every exit is persisted with a detached short context: shutdown must not
	// leave an accepted provider send eligible for blind automatic retry.
	finish := func(state string, result *domains.SendResult, code, message string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := s.store.FinishOperation(ctx, op.ConnectionID, op.ID, state, result, code, message)
		var conflict *domains.Error
		if errors.As(err, &conflict) && conflict.Code == "OPERATION_CONFLICT" {
			// A persisted native own-message echo can prove success before the
			// send RPC finishes. Never downgrade it or count the race as failure.
			current, lookupErr := s.store.GetOperation(ctx, op.ConnectionID, op.ID)
			if lookupErr == nil && current.State == "succeeded" {
				return
			}
		}
		s.workerError(err)
	}
	if s.ctx.Err() != nil {
		finish("queued", nil, "", "")
		return
	}
	d, err := s.store.DeviceByConnection(s.ctx, op.ConnectionID)
	if err != nil {
		finish("failed", nil, "DEVICE_NOT_FOUND", "connection no longer exists")
		return
	}
	e, err := s.entry(d)
	if err != nil {
		finish("queued", nil, "", "")
		return
	}
	e.mu.Lock()
	pause := false
	defer func() {
		e.mu.Unlock()
		if pause {
			s.wait()
		}
	}()
	if e.closed {
		finish("failed", nil, "DEVICE_NOT_FOUND", "connection no longer exists")
		return
	}
	// A logout or deletion can invalidate a claim while this worker waits for
	// the account lock. Recheck the durable state before any provider call.
	current, lookupErr := s.store.GetOperation(s.ctx, op.ConnectionID, op.ID)
	if lookupErr != nil {
		s.workerError(lookupErr)
		finish("queued", nil, "", "") // this worker has not invoked the provider
		return
	}
	if current.State != "sending" {
		return
	}
	state := e.client.Status()
	if state.Auth == "auth_required" || state.Auth == "logged_out" || (!e.desired && state.Auth == "unauthenticated") {
		finish("failed", nil, "AUTH_REQUIRED", "account authentication is required")
		return
	}
	if state.Transport != "connected" {
		finish("queued", nil, "", "")
		pause = true
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.options.SendTimeout)
	started := time.Now()
	var result domains.SendResult
	if op.Request.Kind == "operation" {
		var payload []byte
		payload, err = mutationWirePayload(op.Request)
		if err == nil {
			result.Data, err = e.client.Call(ctx, op.Request.Operation, payload)
		}
		if err == nil && (len(result.Data) > 1<<20 || !json.Valid(result.Data)) {
			err = &domains.Error{Code: "SEND_UNKNOWN", Message: "mutation response was invalid", HTTP: 202, Ambiguous: true}
		}
	} else {
		result, err = e.client.Send(ctx, op.Request)
	}
	cancel()
	var sendErr *domains.Error
	unknown := err != nil && (!errors.As(err, &sendErr) || sendErr.Ambiguous)
	s.observeSend(started, unknown)
	if err == nil {
		finish("succeeded", &result, "", "")
		return
	}
	var de *domains.Error
	if errors.As(err, &de) {
		if de.Ambiguous {
			finish("unknown", nil, "SEND_UNKNOWN", publicErrorMessage("SEND_UNKNOWN"))
			return
		}
		// Only the provider's explicit before-wire classification can requeue.
		if de.Retryable && de.Code == "CONNECTION_UNAVAILABLE" {
			finish("queued", nil, "CONNECTION_UNAVAILABLE", publicErrorMessage(de.Code))
			pause = true
			return
		}
		finish("failed", nil, safeCode(de.Code), publicErrorMessage(de.Code))
		return
	}
	// A generic timeout/error after invoking Send cannot prove that nothing left
	// the socket. Never classify it as safely retryable.
	finish("unknown", nil, "SEND_UNKNOWN", publicErrorMessage("SEND_UNKNOWN"))
}

func (s *Service) reconnectLoop() {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		s.mu.RLock()
		entries := make(map[string]*clientEntry, len(s.clients))
		for id, e := range s.clients {
			entries[id] = e
		}
		s.mu.RUnlock()
		for conn, e := range entries {
			if !e.mu.TryLock() {
				continue
			}
			if e.closed || !e.desired {
				e.mu.Unlock()
				continue
			}
			status := e.client.Status()
			if status.Auth == "auth_required" {
				e.desired = false
				_ = s.store.ClearSession(s.ctx, conn)
				e.mu.Unlock()
				continue
			}
			if status.Transport == "connected" {
				e.failures = 0
				e.mu.Unlock()
				continue
			}
			if time.Now().Before(e.nextConnect) {
				e.mu.Unlock()
				continue
			}
			select {
			case s.reconnectSlots <- struct{}{}:
				s.wg.Add(1)
				go s.connectEntry(conn, e) // entry lock is intentionally transferred to worker
			default:
				e.mu.Unlock()
			}
		}
		if !s.wait() {
			return
		}
	}
}
func (s *Service) connectEntry(conn string, e *clientEntry) {
	defer s.wg.Done()
	defer func() { <-s.reconnectSlots }()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	session, err := s.store.LoadSession(ctx, conn)
	if err == nil && session == nil {
		e.desired = false
		return
	}
	if err == nil {
		err = e.client.Connect(ctx, session, s.sink(conn))
	}
	if err != nil {
		e.failures++
		e.nextConnect = time.Now().Add(reconnectDelay(e.failures))
	} else {
		e.failures = 0
		e.nextConnect = time.Time{}
	}
}

func (s *Service) ListDeliveries(ctx context.Context, id string, limit, offset int) ([]domains.Delivery, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.store.ListDeliveries(ctx, d.ConnectionID, limit, offset)
}
func (s *Service) GetDelivery(ctx context.Context, id, deliveryID string) (domains.Delivery, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Delivery{}, err
	}
	return s.store.GetDelivery(ctx, d.ConnectionID, deliveryID)
}
func (s *Service) ReplayDelivery(ctx context.Context, id, deliveryID string) ([]domains.Delivery, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	delivery, err := s.store.GetDelivery(ctx, d.ConnectionID, deliveryID)
	if err != nil {
		return nil, err
	}
	eventType := eventTypeFromBody(delivery.Body)
	targets := s.targets(d, eventType)
	if len(targets) == 0 {
		return nil, domains.E("NO_WEBHOOK_TARGETS", "no active webhook target accepts this event", 409)
	}
	return s.store.ReplayDelivery(ctx, d.ConnectionID, deliveryID, targets)
}
func (s *Service) RetryDelivery(ctx context.Context, id, deliveryID string) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.store.RetryDelivery(ctx, d.ConnectionID, deliveryID)
	return err
}

func (s *Service) webhookLoop() {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		jobs, err := s.store.ClaimDeliveries(s.ctx, 1, time.Now().UTC())
		s.workerError(err)
		if err == nil && len(jobs) > 0 {
			s.deliver(jobs[0])
			continue
		}
		if !s.wait() {
			return
		}
	}
}
func (s *Service) deliver(d domains.Delivery) {
	update := func(state string, next time.Time, message string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.workerError(s.store.UpdateDelivery(ctx, d.ConnectionID, d.ID, state, next, message))
	}
	if err := validateWebhookURL(d.URL); err != nil {
		update("failed", time.Time{}, "invalid webhook URL")
		return
	}
	device, err := s.store.DeviceByConnection(s.ctx, d.ConnectionID)
	if err != nil {
		var missing *domains.Error
		if errors.As(err, &missing) && missing.Code == "NOT_FOUND" {
			// DeleteDevice atomically cancels its delivery ledger. A stale
			// claim must not revive it or rewrite that terminal audit state.
			return
		}
		if s.ctx.Err() == nil {
			s.workerError(err)
		}
		// Cancellation or a transient DB failure does not revoke the target.
		// Keep the claim recoverable instead of silently pausing a valid event.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.workerError(s.store.ReleaseDeliveryClaim(ctx, d.ConnectionID, d.ID))
		cancel()
		s.wait()
		return
	}
	// Device-target revisions identify the authorization to send to a URL. A URL
	// change must never silently forward old queued payloads to its replacement.
	if d.Revision != device.Webhook.Revision || (d.Device && d.URL != device.Webhook.URL) {
		update("paused", time.Time{}, "webhook target changed; replay explicitly to current targets")
		return
	}
	if d.Device {
		d.Secret = device.Webhook.Secret
	} else {
		found := false
		for _, target := range s.options.GlobalWebhooks {
			if target.URL == d.URL {
				d.Secret = target.Secret
				found = true
				break
			}
		}
		if !found {
			update("paused", time.Time{}, "global webhook target is no longer configured; replay explicitly")
			return
		}
	}
	if strings.TrimSpace(d.Secret) == "" {
		update("failed", time.Time{}, "webhook secret is missing; configure a secret before retrying")
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, strings.NewReader(string(d.Body)))
	if err != nil {
		update("failed", time.Time{}, "invalid webhook request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GoBale-Event-Id", d.EventID)
	req.Header.Set("X-GoBale-Delivery-Id", d.ID)
	req.Header.Set("X-Webhook-Id", d.EventID)
	req.Header.Set("User-Agent", "GoBale-Webhook/1")
	mac := hmac.New(sha256.New, []byte(d.Secret))
	_, _ = mac.Write(d.Body)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	started := time.Now()
	response, err := s.options.WebhookClient.Do(req)
	successful := false
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		successful = response.StatusCode >= 200 && response.StatusCode < 300
	}
	s.observeWebhook(started, !successful)
	if successful {
		update("delivered", time.Time{}, "")
		return
	}
	if s.ctx.Err() != nil {
		update("retry", time.Now().UTC(), "service shutdown interrupted delivery; retry pending")
		return
	}
	if d.Attempts >= 8 {
		update("failed", time.Time{}, "webhook delivery failed after 8 attempts")
		return
	}
	delay := webhookRetryDelay(d.Attempts)
	update("retry", time.Now().UTC().Add(delay), "webhook endpoint did not acknowledge delivery")
}

func webhookRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	ceiling := time.Duration(1<<uint(attempt)) * time.Second
	// Equal jitter avoids synchronized retries while retaining a nonzero floor.
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
}
