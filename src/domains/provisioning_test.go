package domains

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProvisioningValidation(t *testing.T) {
	valid := ProvisionDeviceRequest{DeviceID: "channel-123", WebhookURL: "https://example.test/hook", WebhookSecret: "secret", WebhookEvents: []string{"message"}, WebhookFilter: WebhookFilter{Directions: []string{"incoming"}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, code string
		change     func(*ProvisionDeviceRequest)
	}{
		{"alias", "INVALID_DEVICE_ID", func(r *ProvisionDeviceRequest) { r.DeviceID = "../other" }},
		{"alias length", "INVALID_DEVICE_ID", func(r *ProvisionDeviceRequest) { r.DeviceID = strings.Repeat("a", 65) }},
		{"credentials in URL", "INVALID_WEBHOOK", func(r *ProvisionDeviceRequest) { r.WebhookURL = "https://user:secret@example.test/hook" }},
		{"empty secret", "WEBHOOK_SECRET_REQUIRED", func(r *ProvisionDeviceRequest) { r.WebhookSecret = " " }},
		{"long secret", "INVALID_WEBHOOK", func(r *ProvisionDeviceRequest) { r.WebhookSecret = strings.Repeat("a", 4097) }},
		{"empty event", "INVALID_WEBHOOK", func(r *ProvisionDeviceRequest) { r.WebhookEvents = []string{" "} }},
		{"too many events", "INVALID_WEBHOOK", func(r *ProvisionDeviceRequest) { r.WebhookEvents = make([]string, 101) }},
		{"filter", "INVALID_WEBHOOK_FILTER", func(r *ProvisionDeviceRequest) { r.WebhookFilter.Directions = []string{"unsupported"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := valid
			test.change(&r)
			var err *Error
			if !errors.As(r.Validate(), &err) || err.Code != test.code {
				t.Fatalf("wanted %s, got %v", test.code, err)
			}
		})
	}
	for _, key := range []string{"", " \t", strings.Repeat("a", 257)} {
		if err := ValidateProvisioningKey(key); err == nil {
			t.Fatalf("accepted invalid key length %d", len(key))
		}
	}
	if err := ValidateProvisioningKey(strings.Repeat("a", 256)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"device_id":"channel","webhook_filter":null}`), &ProvisionDeviceRequest{}); err == nil {
		t.Fatal("accepted null webhook filter")
	}
}

func TestProvisioningJSONRejectsUnknownAliasesNullAndDuplicates(t *testing.T) {
	for _, body := range []string{
		`{"device_id":"channel","webhook_filters":{}}`,
		`{"device_id":"channel","Device_ID":"other"}`,
		`{"DeviceID":"channel"}`,
		`{"device_id":"channel","Webhook_URL":"https://example.test"}`,
		`{"device_id":"channel","device_id":"other"}`,
		`{"device_id":null}`,
		`{"device_id":"channel","webhook_url":null}`,
		`{"device_id":"channel","webhook_secret":null}`,
		`{"device_id":"channel","webhook_events":null}`,
		`{"device_id":"channel","webhook_filter":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			if err := json.Unmarshal([]byte(body), &ProvisionDeviceRequest{}); err == nil {
				t.Fatal("accepted ambiguous provisioning body")
			}
		})
	}
	for _, body := range []string{`{"device_id":"channel"}`, `{"device_id":"channel","webhook_events":[],"webhook_filter":{}}`} {
		var request ProvisionDeviceRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if err := request.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
