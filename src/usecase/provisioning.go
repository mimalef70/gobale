package usecase

import (
	"context"

	"github.com/mimalef70/goomni/src/domains"
)

// ProvisionDevice provisions a connection without contacting the provider. Its
// mandatory key binds the initial request to one immutable device lifetime.
func (s *Service) ProvisionDevice(ctx context.Context, request domains.ProvisionDeviceRequest, key string) (domains.Device, bool, error) {
	if err := domains.ValidateProvisioningKey(key); err != nil {
		return domains.Device{}, false, err
	}
	if err := request.Validate(); err != nil {
		return domains.Device{}, false, err
	}
	contract, err := s.provider(request.Provider)
	if err != nil {
		return domains.Device{}, false, err
	}
	if err = validateFilter(contract, request.WebhookFilter); err != nil {
		return domains.Device{}, false, err
	}
	return s.store.ProvisionDevice(ctx, request, key)
}
