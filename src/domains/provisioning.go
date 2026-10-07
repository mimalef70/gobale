package domains

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// ProvisionDeviceRequest describes only the initial device configuration. A
// replay checks this request, but never resets a device's later configuration.
// WebhookSecret is input-only; never include this request in logs or responses.
type ProvisionDeviceRequest struct {
	DeviceID      string        `json:"device_id"`
	WebhookURL    string        `json:"webhook_url,omitempty"`
	WebhookSecret string        `json:"webhook_secret,omitempty"`
	WebhookEvents []string      `json:"webhook_events,omitempty"`
	WebhookFilter WebhookFilter `json:"webhook_filter,omitempty"`
}

func (r *ProvisionDeviceRequest) UnmarshalJSON(data []byte) error {
	if err := ValidateJSONObject(data); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return E("INVALID_REQUEST", "invalid provisioning request", 400)
	}
	for name, value := range fields {
		switch name {
		case "device_id", "webhook_url", "webhook_secret", "webhook_events", "webhook_filter":
		default:
			return E("INVALID_REQUEST", "unsupported provisioning field", 400)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			if name == "webhook_filter" {
				return E("INVALID_WEBHOOK_FILTER", "webhook_filter must be an object", 400)
			}
			return E("INVALID_REQUEST", "provisioning fields cannot be null", 400)
		}
	}
	type plain ProvisionDeviceRequest
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return E("INVALID_REQUEST", "invalid provisioning request", 400)
	}
	*r = ProvisionDeviceRequest(decoded)
	return nil
}

var provisioningAlias = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func ValidateProvisioningKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return E("IDEMPOTENCY_KEY_REQUIRED", "provide an Idempotency-Key for device provisioning", 400)
	}
	if len(key) > 256 {
		return E("INVALID_IDEMPOTENCY_KEY", "idempotency key exceeds 256 bytes", 400)
	}
	return nil
}

func (r ProvisionDeviceRequest) Validate() error {
	if !provisioningAlias.MatchString(r.DeviceID) {
		return E("INVALID_DEVICE_ID", "device id must contain 1-64 letters, digits, dots, underscores or hyphens", 400)
	}
	if r.WebhookURL != "" {
		u, err := url.Parse(r.WebhookURL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return E("INVALID_WEBHOOK", "webhook_url must be an HTTP(S) URL without credentials or fragment", 400)
		}
		if strings.TrimSpace(r.WebhookSecret) == "" {
			return E("WEBHOOK_SECRET_REQUIRED", "configure a non-empty secret before enabling a device webhook", 400)
		}
	}
	if len(r.WebhookSecret) > 4096 {
		return E("INVALID_WEBHOOK", "webhook secret exceeds 4096 bytes", 400)
	}
	if len(r.WebhookEvents) > 100 {
		return E("INVALID_WEBHOOK", "too many event filters", 400)
	}
	for _, name := range r.WebhookEvents {
		if strings.TrimSpace(name) == "" || len(name) > 128 {
			return E("INVALID_WEBHOOK", "invalid event filter", 400)
		}
	}
	return r.WebhookFilter.Validate()
}
