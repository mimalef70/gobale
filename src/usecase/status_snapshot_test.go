package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func TestStatusSnapshotIsLocalAndKeepsConnectionIdentity(t *testing.T) {
	ctx := context.Background()
	var factories atomic.Int32
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client {
		factories.Add(1)
		return &fakeClient{}
	})
	d := mustDevice(t, s, "snapshot")
	status, err := s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, d.ID, status.DeviceID)
	require.Equal(t, d.InstanceToken(), status.InstanceID)
	require.Empty(t, status.AccountID)
	require.Nil(t, status.Challenge)
	require.Equal(t, "auth_required", status.Auth)
	require.Equal(t, "disconnected", status.Transport)
	require.Equal(t, "degraded", status.Recovery)
	require.False(t, status.ServerTime.IsZero())
	require.Zero(t, factories.Load())
	bound, err := s.BindDevice(ctx, d)
	require.NoError(t, err)
	require.NoError(t, s.DeleteDevice(ctx, d.ID))
	replacement := mustDevice(t, s, d.ID)
	_, err = s.Status(bound, d.ID)
	codeIs(t, err, "NOT_FOUND")
	status, err = s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, replacement.InstanceToken(), status.InstanceID)
	require.NotEqual(t, d.InstanceToken(), status.InstanceID)
}

func TestStatusSnapshotChallengeExpiryAndAuthentication(t *testing.T) {
	ctx := context.Background()
	f := &adminAuthClient{fakeClient: &fakeClient{}, response: domains.Challenge{ExpiresAt: time.Now().Add(time.Minute), AvailableSendCodeTypes: []int32{1}}}
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "snapshot")
	ch, err := s.StartAuth(ctx, d.ID, "+15550000123")
	require.NoError(t, err)
	status, err := s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_code", status.Auth)
	require.Equal(t, ch.ID, status.Challenge.ID)
	require.Equal(t, "••••••••23", status.Challenge.MaskedPhone)
	status.Challenge.AvailableSendCodeTypes[0] = 99
	status, err = s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Challenge.AvailableSendCodeTypes[0])
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "+15550000123")
	_, err = s.SubmitCode(ctx, d.ID, ch.ID, "synthetic")
	require.NoError(t, err)
	status, err = s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_password", status.Auth)
	require.Equal(t, ch.ID, status.Challenge.ID)
	e, err := s.entry(d)
	require.NoError(t, err)
	e.authMetaMu.Lock()
	e.challenge.ExpiresAt = time.Now().Add(-time.Second)
	e.authMetaMu.Unlock()
	status, err = s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, "auth_required", status.Auth)
	require.Nil(t, status.Challenge)
	ch, err = s.StartAuth(ctx, d.ID, "+15550000123")
	require.NoError(t, err)
	_, err = s.SubmitCode(ctx, d.ID, ch.ID, "synthetic")
	require.NoError(t, err)
	_, err = s.SubmitPassword(ctx, d.ID, ch.ID, "synthetic-password")
	require.NoError(t, err)
	status, err = s.Status(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, "1001", status.AccountID)
	require.Equal(t, "authenticated", status.Auth)
	require.Equal(t, "connected", status.Transport)
	require.Equal(t, "degraded", status.Recovery)
	require.Nil(t, status.Challenge)
	raw, err = json.Marshal(status)
	require.NoError(t, err)
	for _, forbidden := range []string{"synthetic-session", "synthetic-password", "token", "transaction_hash"} {
		require.False(t, strings.Contains(string(raw), forbidden))
	}
}
