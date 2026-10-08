package usecase

import (
	"context"
	"time"
	"unicode"

	"github.com/mimalef70/gobale/src/domains"
)

// DevicesOverview is a local-only snapshot. Polling it must not instantiate a
// provider client, connect an account or issue any provider RPC.
func (s *Service) DevicesOverview(ctx context.Context) (domains.DevicesOverview, error) {
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return domains.DevicesOverview{}, err
	}
	counts, err := s.store.DeliveryCounts(ctx)
	if err != nil {
		return domains.DevicesOverview{}, err
	}
	now := time.Now().UTC()
	result := domains.DevicesOverview{ServerTime: now, Devices: make([]domains.DeviceOverview, 0, len(devices))}
	for _, d := range devices {
		status := domains.ConnectionStatus{Auth: "auth_required", Transport: "disconnected", Recovery: "degraded"}
		s.mu.RLock()
		e := s.clients[d.ConnectionID]
		s.mu.RUnlock()
		if e != nil {
			status = e.adminStatus(now)
		}
		result.Devices = append(result.Devices, domains.DeviceOverview{Device: d, Status: status, Deliveries: counts[d.ConnectionID]})
	}
	return result, nil
}

func (s *Service) LoginState(ctx context.Context, id string) (domains.LoginState, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.LoginState{}, err
	}
	now := time.Now().UTC()
	result := domains.LoginState{State: "auth_required", ServerTime: now}
	s.mu.RLock()
	e := s.clients[d.ConnectionID]
	s.mu.RUnlock()
	if e == nil {
		return result, nil
	}
	status, challenge := e.adminSnapshot(now)
	result.State = status.Auth
	result.Challenge = challenge
	return result, nil
}

func (e *clientEntry) adminStatus(now time.Time) domains.ConnectionStatus {
	status, _ := e.adminSnapshot(now)
	return status
}

func (e *clientEntry) adminSnapshot(now time.Time) (domains.ConnectionStatus, *domains.PublicChallenge) {
	// Copy once so a refresh never advertises an awaiting state with no usable
	// challenge. This read does not wait for the lifecycle/provider operation lock.
	challenge := e.publicChallenge(now)
	e.clientMu.RLock()
	client := e.client
	e.clientMu.RUnlock()
	status := cleanStatus(client.Status())
	// Expired provider challenges can remain in a client's status until another
	// authentication call. Never advertise them as usable in a refreshed form.
	if status.Auth == "awaiting_code" || status.Auth == "awaiting_password" {
		if challenge == nil {
			status.Auth = "auth_required"
		}
	} else {
		challenge = nil
	}
	return status, challenge
}

// Status returns identity, authentication, transport, recovery and public login
// metadata together without constructing a client, reconnecting or making RPCs.
func (s *Service) Status(ctx context.Context, id string) (domains.DeviceStatus, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.DeviceStatus{}, err
	}
	now := time.Now().UTC()
	result := domains.DeviceStatus{ConnectionStatus: domains.ConnectionStatus{Auth: "auth_required", Transport: "disconnected", Recovery: "degraded"}, ServerTime: now}
	s.mu.RLock()
	e := s.clients[d.ConnectionID]
	s.mu.RUnlock()
	if e != nil {
		result.ConnectionStatus, result.Challenge = e.adminSnapshot(now)
	}
	// Recheck this exact connection after the snapshot, including a binding that
	// completed concurrently. Deletion/reuse must not resolve another alias.
	d, err = s.store.DeviceByConnection(ctx, d.ConnectionID)
	if err != nil {
		return domains.DeviceStatus{}, err
	}
	result.DeviceID, result.InstanceID, result.AccountID = d.ID, d.InstanceToken(), d.AccountID
	return result, nil
}

func (e *clientEntry) publicChallenge(now time.Time) *domains.PublicChallenge {
	e.authMetaMu.Lock()
	defer e.authMetaMu.Unlock()
	if e.challenge != nil && !now.Before(e.challenge.ExpiresAt) {
		e.challenge = nil
	}
	if e.challenge == nil {
		return nil
	}
	copy := *e.challenge
	copy.AvailableSendCodeTypes = append([]int32{}, copy.AvailableSendCodeTypes...)
	return &copy
}

func (e *clientEntry) clearAuthMetadata() {
	e.authMetaMu.Lock()
	e.challenge = nil
	e.resendAvailableAt = time.Time{}
	e.authMetaMu.Unlock()
}

func maskedPhone(phone string) string {
	// Keep only the last two digits. No raw caller text can become UI markup or
	// reveal an international prefix with a short input.
	var digits []rune
	for _, r := range phone {
		if unicode.IsDigit(r) {
			digits = append(digits, r)
		}
	}
	if len(digits) < 3 {
		return "••••"
	}
	return "••••••••" + string(digits[len(digits)-2:])
}

func (s *Service) WebhookDetails(ctx context.Context, id string) (domains.WebhookDetails, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.WebhookDetails{}, err
	}
	result := domains.WebhookDetails{WebhookConfig: d.Webhook, SecretConfigured: d.Webhook.Secret != "", RoutingMode: "none", RoutingRules: []domains.WebhookRoutingRule{}}
	if d.Webhook.URL != "" {
		result.RoutingMode = "device"
		result.RoutingRules = append(result.RoutingRules, domains.WebhookRoutingRule{Source: "device", URL: d.Webhook.URL, Events: append([]string{}, d.Webhook.Events...), Filter: d.Webhook.Filter, SecretConfigured: d.Webhook.Secret != ""})
	}
	if d.Webhook.URL == "" || s.options.MergeGlobal {
		for _, target := range s.options.GlobalWebhooks {
			result.RoutingRules = append(result.RoutingRules, domains.WebhookRoutingRule{Source: "global", URL: target.URL, Events: append([]string{}, s.options.GlobalWebhookEvents...), SecretConfigured: target.Secret != ""})
		}
		if len(s.options.GlobalWebhooks) > 0 {
			if d.Webhook.URL == "" {
				result.RoutingMode = "global"
			} else {
				result.RoutingMode = "merged"
			}
		}
	}
	return result, nil
}

func (s *Service) ListDeliveriesFiltered(ctx context.Context, id string, limit, offset int, state string, includePayload bool) ([]domains.Delivery, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.store.ListDeliveriesFiltered(ctx, d.ConnectionID, limit, offset, state, includePayload)
}
