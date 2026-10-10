package usecase

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/stretchr/testify/require"
)

type reconnectTestContract struct {
	bale.Contract
	provider domains.Provider
}

func (c reconnectTestContract) Descriptor() domains.ProviderDescriptor {
	return domains.ProviderDescriptor{ID: c.provider, Name: string(c.provider), Enabled: true}
}
func (c reconnectTestContract) ValidateSession(s *domains.Session) error {
	if s.Provider != c.provider || s.Version != 1 {
		return domains.E("INVALID_SESSION", "synthetic session mismatch", 500)
	}
	return nil
}

func reconnectTestRegistry(t *testing.T, factory domains.ClientFactory) *domains.ProviderRegistry {
	t.Helper()
	var registrations []domains.ProviderRegistration
	for _, p := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
		registrations = append(registrations, domains.ProviderRegistration{Contract: reconnectTestContract{provider: p}, Factory: factory})
	}
	r, err := domains.NewProviderRegistry(registrations...)
	require.NoError(t, err)
	return r
}

func TestReconnectHungProvidersLeaveHealthyProviderCapacity(t *testing.T) {
	started := make(chan domains.Provider, 10)
	factory := func(d domains.Device) domains.Client {
		f := &fakeClient{status: domains.ConnectionStatus{Auth: "authenticated", Transport: "disconnected"}}
		f.connectFn = func(ctx context.Context, _ *domains.Session, _ domains.Sink) error {
			started <- d.Provider
			if d.Provider != domains.ProviderRubika {
				<-ctx.Done()
				return ctx.Err()
			}
			f.mu.Lock()
			f.status = readyStatus()
			f.mu.Unlock()
			return nil
		}
		return f
	}
	s, st := testService(t, Options{Providers: reconnectTestRegistry(t, factory), ReconnectWorkers: 4}, nil)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()
	for _, p := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
		for i := range 2 {
			d, err := st.CreateDevice(s.ctx, fmt.Sprintf("%s-%d", p, i), p)
			require.NoError(t, err)
			require.NoError(t, st.SaveSession(s.ctx, d.ConnectionID, &domains.Session{Provider: p, Version: 1, UserID: "123", Token: "synthetic"}))
			e, err := s.entry(d)
			require.NoError(t, err)
			e.desired = true
		}
	}
	cursor := reconnectCursor{}
	s.reconnectPass(&cursor)
	counts := map[domains.Provider]int{}
	for range 3 {
		select {
		case p := <-started:
			counts[p]++
		case <-time.After(2 * time.Second):
			t.Fatal("healthy provider starved behind hung reconnects")
		}
	}
	require.Equal(t, map[domains.Provider]int{domains.ProviderBale: 1, domains.ProviderEitaa: 1, domains.ProviderRubika: 1}, counts)
	// Rubika's second account proceeds while both other providers remain stuck.
	require.Eventually(t, func() bool {
		s.reconnectPass(&cursor)
		select {
		case p := <-started:
			require.Equal(t, domains.ProviderRubika, p)
			return true
		default:
			return false
		}
	}, 2*time.Second, time.Millisecond)
	s.cancel()
	s.wg.Wait()
	require.Zero(t, s.WorkerMetrics().ReconnectBusy)
}

func TestReconnectSingleSlotRotatesProvidersAndConnections(t *testing.T) {
	started := make(chan string, 10)
	factory := func(d domains.Device) domains.Client {
		return &fakeClient{status: domains.ConnectionStatus{Auth: "authenticated", Transport: "disconnected"}, connectFn: func(context.Context, *domains.Session, domains.Sink) error {
			// Stay disconnected, so fairness must not rely on successful connection.
			started <- d.ID
			return nil
		}}
	}
	s, st := testService(t, Options{Providers: reconnectTestRegistry(t, factory), ReconnectWorkers: 1}, nil)
	for _, p := range []domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika} {
		for i := range 2 {
			d, err := st.CreateDevice(s.ctx, fmt.Sprintf("%s-%d", p, i), p)
			require.NoError(t, err)
			require.NoError(t, st.SaveSession(s.ctx, d.ConnectionID, &domains.Session{Provider: p, Version: 1, UserID: "123", Token: "synthetic"}))
			e, err := s.entry(d)
			require.NoError(t, err)
			e.desired = true
		}
	}
	cursor := reconnectCursor{}
	seen := map[string]bool{}
	for range 6 {
		s.reconnectPass(&cursor)
		s.wg.Wait()
		select {
		case id := <-started:
			require.False(t, seen[id], "a reconnecting connection ran twice before its peers")
			seen[id] = true
		default:
			t.Fatal("no reconnect started")
		}
	}
	require.Len(t, seen, 6)
}
