package eitaameow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mimalef70/goomni/src/domains"
)

func (c *Client) invoke(ctx context.Context, method string, params object, mutating, anonymous bool) (object, error) {
	return c.invokeWithProfile(ctx, method, params, mutating, anonymous, false)
}
func (c *Client) invokeWithProfile(ctx context.Context, method string, params object, mutating, anonymous, upload bool) (object, error) {
	c.mu.RLock()
	token := c.session.Token
	c.mu.RUnlock()
	response, err := c.invokeProfile(ctx, method, params, mutating, anonymous, upload)
	var de *domains.Error
	if anonymous || !errors.As(err, &de) || de.Code != "SESSION_REFRESH_REQUIRED" {
		return response, err
	}
	if renewalErr := c.renewSession(ctx, token); renewalErr != nil {
		if mutating {
			return nil, &domains.Error{Code: "SEND_UNKNOWN", Message: "write outcome is unknown after session renewal failed", HTTP: 502, Ambiguous: true}
		}
		return nil, renewalErr
	}
	// A renewed token is not acceptance proof for a previously transmitted
	// mutation. Retain its unknown outcome instead of repeating the request.
	if mutating {
		return nil, &domains.Error{Code: "SEND_UNKNOWN", Message: "write outcome requires reconciliation after session renewal", HTTP: 502, Ambiguous: true}
	}
	return c.invokeProfile(ctx, method, params, false, false, upload)
}

func (c *Client) renewSession(ctx context.Context, previous string) error {
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.mu.RLock()
	next := c.session
	c.mu.RUnlock()
	if next.Token != previous {
		return nil // a concurrent request already committed this rotation
	}
	if c.cfg.PersistSession == nil {
		return domains.E("SESSION_PERSISTENCE_REQUIRED", "session renewal requires durable storage", 503)
	}
	response, err := c.invokeOnce(ctx, "eitaaRefreshToken", object{"app_info": object{
		"_": "eitaaAppInfo", "build_version": c.cfg.APIID, "device_model": "GoOmni", "system_version": "Go", "app_version": "5.1 K", "lang_code": "en", "sign": "",
	}}, false, false)
	if err != nil {
		return err
	}
	if response.str("_") != "eitaa_updates_token" || response.str("token") == "" || len(response.str("token")) > 8192 || response.num("date") <= 0 || response.num("expire") < 0 {
		return protocolError()
	}
	next.Token = response.str("token")
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = c.cfg.PersistSession(ctx, &domains.Session{Provider: domains.ProviderEitaa, Version: sessionVersion, UserID: next.UserID, Data: raw}); err != nil {
		return err
	}
	c.mu.Lock()
	c.session = next
	c.mu.Unlock()
	return nil
}
