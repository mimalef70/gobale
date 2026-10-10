package usecase

import (
	"context"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestBoundDeviceContextRejectsDeletedAliasReplacement(t *testing.T) {
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	original := mustDevice(t, s, "reused")
	ctx, err := s.BindDevice(context.Background(), original)
	require.NoError(t, err)
	// Implicit selection remains tied to the same connection after another
	// device is added, and explicit selectors must still match the binding.
	other := mustDevice(t, s, "other")
	selected, err := s.ResolveDevice(ctx, "")
	require.NoError(t, err)
	require.Equal(t, original.ConnectionID, selected.ConnectionID)
	_, err = s.ResolveDevice(ctx, other.ID)
	codeIs(t, err, "DEVICE_SCOPE_MISMATCH")
	_, err = s.ResolveDevice(ctx, "does-not-exist")
	codeIs(t, err, "DEVICE_SCOPE_MISMATCH")
	_, err = s.BindDevice(ctx, other)
	codeIs(t, err, "DEVICE_SCOPE_MISMATCH")
	require.NoError(t, s.DeleteDevice(context.Background(), original.ID))
	replacement := mustDevice(t, s, original.ID)
	require.NotEqual(t, original.ConnectionID, replacement.ConnectionID)
	for _, selector := range []string{"", original.ID} {
		_, err = s.ResolveDevice(ctx, selector)
		codeIs(t, err, "NOT_FOUND")
	}
	_, err = s.BindDevice(ctx, replacement)
	codeIs(t, err, "DEVICE_SCOPE_MISMATCH")
	// Even delayed binding of a previously selected object cannot re-resolve
	// its alias to the new connection. Cleanup cannot delete the replacement.
	_, err = s.BindDevice(context.Background(), original)
	codeIs(t, err, "NOT_FOUND")
	err = s.DeleteDevice(ctx, original.ID)
	codeIs(t, err, "NOT_FOUND")
	current, err := s.GetDevice(context.Background(), original.ID)
	require.NoError(t, err)
	require.Equal(t, replacement.ConnectionID, current.ConnectionID)
}
