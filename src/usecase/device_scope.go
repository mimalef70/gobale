package usecase

import (
	"context"

	"github.com/mimalef70/goomni/src/domains"
)

type deviceScopeKey struct{}

// A request keeps the connection it selected even if its public alias is later
// deleted and reused. Do not put credentials or a stale device configuration in
// the context: each lookup still checks that this exact connection is active.
type deviceScope struct {
	alias, connection string
}

// BindDevice pins a previously selected device for all usecase calls made with
// the returned context, including nested calls and operation polling. It never
// resolves the alias again. Transports must bind before reading a request body
// or performing any other work that can block after selecting the account.
func (s *Service) BindDevice(ctx context.Context, device domains.Device) (context.Context, error) {
	if previous, ok := ctx.Value(deviceScopeKey{}).(deviceScope); ok &&
		(previous.alias != device.ID || previous.connection != device.ConnectionID) {
		return nil, domains.E("DEVICE_SCOPE_MISMATCH", "request is bound to another connection", 409)
	}
	current, err := s.store.DeviceByConnection(ctx, device.ConnectionID)
	if err != nil {
		return nil, err
	}
	if current.ID != device.ID {
		return nil, domains.E("DEVICE_SCOPE_MISMATCH", "device alias does not match the selected connection", 409)
	}
	return context.WithValue(ctx, deviceScopeKey{}, deviceScope{alias: current.ID, connection: current.ConnectionID}), nil
}
