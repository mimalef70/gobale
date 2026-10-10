package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestSendWorkerMetricCoversProviderCallAndReturnsIdle(t *testing.T) {
	f := &fakeClient{status: readyStatus()}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "synthetic")
	_, err := s.Send(context.Background(), d.ID, sendReq("synthetic"), "metric-test")
	require.NoError(t, err)
	f.sendFn = func(_ context.Context, request domains.SendRequest) (domains.SendResult, error) {
		require.Equal(t, 1, s.WorkerMetrics().SendBusy)
		return domains.SendResult{MessageID: request.RequestID}, nil
	}
	s.processOperation(claimOne(t, st))
	m := s.WorkerMetrics()
	require.Zero(t, m.SendBusy)
	require.EqualValues(t, 1, m.SendAttempts)
	require.EqualValues(t, 1, m.SendDurationBuckets["+Inf"])
}

func TestReconnectMetricsTrackActualFailureAndReleaseWorker(t *testing.T) {
	f := &fakeClient{}
	s, st := testService(t, Options{ReconnectWorkers: 1}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "synthetic")
	require.NoError(t, st.SaveSession(context.Background(), d.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "123", Token: "synthetic-token"}))
	e, err := s.entry(d)
	require.NoError(t, err)
	f.connectFn = func(context.Context, *domains.Session, domains.Sink) error {
		require.Equal(t, 1, s.WorkerMetrics().ReconnectBusy)
		return errors.New("synthetic failure")
	}
	e.mu.Lock()
	s.reconnectPool.SetProviders([]domains.Provider{d.Provider})
	release, acquired := s.reconnectPool.TryAcquireFor(d.Provider, d.ConnectionID)
	require.True(t, acquired)
	s.wg.Add(1)
	s.connectEntry(d.ConnectionID, e, release)
	m := s.WorkerMetrics()
	require.Zero(t, m.ReconnectBusy)
	require.EqualValues(t, 1, m.ReconnectAttempts)
	require.EqualValues(t, 1, m.ReconnectFailures)
	require.EqualValues(t, 1, m.ReconnectDurationBuckets["+Inf"])
	require.NotZero(t, e.nextConnect)
}
