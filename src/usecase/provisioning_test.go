package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestProvisionDeviceValidatesAndDoesNotContactProvider(t *testing.T) {
	s, st := testService(t, Options{}, func(domains.Device) domains.Client {
		t.Error("provisioning must not construct or contact a provider client")
		return nil
	})
	ctx := context.Background()
	request := domains.ProvisionDeviceRequest{Provider: domains.ProviderBale, DeviceID: "channel", WebhookURL: "https://example.test/hook", WebhookSecret: "synthetic-secret"}
	for _, test := range []struct {
		request domains.ProvisionDeviceRequest
		key     string
		code    string
	}{
		{request, "", "IDEMPOTENCY_KEY_REQUIRED"},
		{domains.ProvisionDeviceRequest{Provider: domains.ProviderBale, DeviceID: "channel", WebhookURL: "https://example.test/hook"}, "key", "WEBHOOK_SECRET_REQUIRED"},
	} {
		_, _, err := s.ProvisionDevice(ctx, test.request, test.key)
		var de *domains.Error
		if !errors.As(err, &de) || de.Code != test.code {
			t.Fatalf("wanted %s, got %v", test.code, err)
		}
	}
	devices, err := st.ListDevices(ctx)
	if err != nil || len(devices) != 0 {
		t.Fatalf("invalid provisioning left devices: %v, %v", devices, err)
	}
	d, replay, err := s.ProvisionDevice(ctx, request, "key")
	if err != nil || replay {
		t.Fatalf("provision: replay=%v error=%v", replay, err)
	}
	again, replay, err := s.ProvisionDevice(ctx, request, "key")
	if err != nil || !replay || again.ConnectionID != d.ConnectionID {
		t.Fatalf("provision replay: replay=%v error=%v", replay, err)
	}
}
