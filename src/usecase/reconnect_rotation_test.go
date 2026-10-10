package usecase

import (
	"context"
	"sync"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

type reconnectRotatingClient struct {
	fakeClient
	rotationMu   sync.Mutex
	persister    domains.SessionPersister
	disconnectFn func(context.Context) error
}

func (c *reconnectRotatingClient) SetSessionPersister(p domains.SessionPersister) {
	c.rotationMu.Lock()
	c.persister = p
	c.rotationMu.Unlock()
}

func (c *reconnectRotatingClient) capturedPersister() domains.SessionPersister {
	c.rotationMu.Lock()
	defer c.rotationMu.Unlock()
	return c.persister
}

func (c *reconnectRotatingClient) Disconnect(ctx context.Context) error {
	c.rotationMu.Lock()
	fn := c.disconnectFn
	c.disconnectFn = nil
	c.rotationMu.Unlock()
	if fn != nil {
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return c.fakeClient.Disconnect(ctx)
}

func TestReconnectRetiresRotationBeforeStopAndRebindsSameClient(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		name := "explicit"
		if automatic {
			name = "worker"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			client := &reconnectRotatingClient{}
			s, store := testService(t, Options{}, func(domains.Device) domains.Client { return client })
			d := mustDevice(t, s, "rotating")
			session := func(token string) *domains.Session {
				return &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "42", Token: token}
			}
			require.NoError(t, store.SaveSession(ctx, d.ConnectionID, session("before-stop")))
			e, err := s.entry(d)
			require.NoError(t, err)
			e.desired = true
			e.setPersistenceActive(true)
			old := client.capturedPersister()
			require.NotNil(t, old)
			var retiredDuringStop error
			client.disconnectFn = func(ctx context.Context) error {
				retiredDuringStop = old(ctx, session("stale-during-stop"))
				// Model the durable completion joined by Disconnect. Reloading
				// before this boundary would reconnect with the previous token.
				return store.UpdateSession(ctx, d.ConnectionID, session("joined-token"))
			}
			var connectedToken string
			client.connectFn = func(_ context.Context, got *domains.Session, _ domains.Sink) error {
				connectedToken = got.Token
				client.mu.Lock()
				client.status = readyStatus()
				client.mu.Unlock()
				return nil
			}
			if automatic {
				e.mu.Lock()
				s.wg.Add(1)
				s.connectEntry(d.ConnectionID, e, func() {})
				require.Zero(t, e.failures)
			} else {
				require.NoError(t, s.Reconnect(ctx, d.ID))
			}
			codeIs(t, retiredDuringStop, "SESSION_RETIRED")
			require.Equal(t, "joined-token", connectedToken)
			require.Same(t, client, e.client)
			codeIs(t, old(ctx, session("stale-after-reconnect")), "SESSION_RETIRED")
			require.NoError(t, client.capturedPersister()(ctx, session("new-generation")))
			saved, err := store.LoadSession(ctx, d.ConnectionID)
			require.NoError(t, err)
			require.Equal(t, "new-generation", saved.Token)
		})
	}
}

func TestShutdownRetiresRotationBeforeDisconnect(t *testing.T) {
	ctx := context.Background()
	client := &reconnectRotatingClient{}
	s, store := testService(t, Options{}, func(domains.Device) domains.Client { return client })
	d := mustDevice(t, s, "shutdown-rotation")
	session := &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "42", Token: "retained"}
	require.NoError(t, store.SaveSession(ctx, d.ConnectionID, session))
	e, err := s.entry(d)
	require.NoError(t, err)
	e.setPersistenceActive(true)
	old := client.capturedPersister()
	var duringStop error
	client.disconnectFn = func(ctx context.Context) error {
		duringStop = old(ctx, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "42", Token: "stale"})
		return nil
	}
	require.NoError(t, s.Close(ctx))
	codeIs(t, duringStop, "SESSION_RETIRED")
	codeIs(t, old(ctx, session), "SESSION_RETIRED")
	saved, err := store.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, "retained", saved.Token)
}
