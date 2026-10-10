package workpool

import (
	"context"
	"errors"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func pending(t *testing.T, p *Pool, want int) {
	t.Helper()
	require.Eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.pending == want }, time.Second, time.Millisecond)
}
func empty(t *testing.T, p *Pool) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Zero(t, p.active)
	require.Zero(t, p.pending)
	for _, s := range p.states {
		require.Zero(t, s.active)
		require.Empty(t, s.queues)
		require.Empty(t, s.order)
	}
}
func TestSingleProviderUsesCapacityAndReleaseIsIdempotent(t *testing.T) {
	p := New(4)
	p.SetProviders([]domains.Provider{domains.ProviderBale})
	var releases []func()
	for i := 0; i < 4; i++ {
		r, ok := p.TryAcquire(domains.ProviderBale)
		require.True(t, ok)
		releases = append(releases, r)
	}
	_, ok := p.TryAcquire(domains.ProviderBale)
	require.False(t, ok)
	for _, r := range releases {
		r()
		r()
	}
	empty(t, p)
}
func TestStalledProviderCannotConsumeOtherProviderReservations(t *testing.T) {
	p := New(4)
	p.SetProviders([]domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika})
	bale, ok := p.TryAcquire(domains.ProviderBale)
	require.True(t, ok)
	_, ok = p.TryAcquire(domains.ProviderBale)
	require.False(t, ok)
	eitaa, ok := p.TryAcquire(domains.ProviderEitaa)
	require.True(t, ok)
	rubika, ok := p.TryAcquire(domains.ProviderRubika)
	require.True(t, ok)
	_, ok = p.TryAcquire(domains.ProviderEitaa)
	require.False(t, ok)
	bale()
	eitaa()
	rubika()
	empty(t, p)
}
func TestProviderThenConnectionRoundRobinWithFIFO(t *testing.T) {
	p := New(1)
	p.SetProviders([]domains.Provider{domains.ProviderBale, domains.ProviderEitaa, domains.ProviderRubika})
	hold, ok := p.TryAcquire(domains.ProviderBale)
	require.True(t, ok)
	type result struct {
		id      string
		release func()
		err     error
	}
	got := make(chan result, 5)
	jobs := []struct {
		id, connection string
		provider       domains.Provider
	}{{"bale-A1", "A", domains.ProviderBale}, {"bale-A2", "A", domains.ProviderBale}, {"bale-B1", "B", domains.ProviderBale}, {"eitaa-C", "C", domains.ProviderEitaa}, {"rubika-D", "D", domains.ProviderRubika}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i, job := range jobs {
		go func() { r, err := p.AcquireFor(ctx, job.provider, job.connection); got <- result{job.id, r, err} }()
		pending(t, p, i+1)
	}
	_, ok = p.TryAcquireFor(domains.ProviderBale, "new")
	require.False(t, ok)
	hold()
	for _, want := range []string{"eitaa-C", "rubika-D", "bale-A1", "bale-B1", "bale-A2"} {
		select {
		case result := <-got:
			require.NoError(t, result.err)
			require.Equal(t, want, result.id)
			result.release()
		case <-ctx.Done():
			t.Fatal("work did not drain")
		}
	}
	empty(t, p)
}
func TestCancellationAndRemovedProviderDoNotLeakCapacity(t *testing.T) {
	p := New(1)
	p.SetProviders([]domains.Provider{domains.ProviderBale, domains.ProviderRubika})
	hold, ok := p.TryAcquire(domains.ProviderBale)
	require.True(t, ok)
	ctx, cancel := context.WithCancel(context.Background())
	failed := make(chan error, 1)
	go func() { _, err := p.AcquireFor(ctx, domains.ProviderRubika, "cancel"); failed <- err }()
	pending(t, p, 1)
	cancel()
	require.ErrorIs(t, <-failed, context.Canceled)
	pending(t, p, 0)
	go func() { _, err := p.AcquireFor(context.Background(), domains.ProviderRubika, "removed"); failed <- err }()
	pending(t, p, 1)
	p.SetProviders([]domains.Provider{domains.ProviderBale})
	var domain *domains.Error
	require.ErrorAs(t, <-failed, &domain)
	require.Equal(t, "FEATURE_NOT_SUPPORTED", domain.Code)
	_, ok = p.TryAcquire(domains.ProviderBale)
	require.False(t, ok, "reconfiguration must not preempt active permits")
	hold()
	r, ok := p.TryAcquire(domains.ProviderBale)
	require.True(t, ok)
	r()
	empty(t, p)
}
func TestReconfigurationWaitsForExistingWorkWithoutPreempting(t *testing.T) {
	p := New(4)
	p.SetProviders([]domains.Provider{domains.ProviderBale})
	var releases []func()
	for i := 0; i < 4; i++ {
		r, ok := p.TryAcquire(domains.ProviderBale)
		require.True(t, ok)
		releases = append(releases, r)
	}
	p.SetProviders([]domains.Provider{domains.ProviderBale, domains.ProviderEitaa})
	_, ok := p.TryAcquire(domains.ProviderEitaa)
	require.False(t, ok, "global bound remains in force")
	releases[0]()
	r, ok := p.TryAcquire(domains.ProviderEitaa)
	require.True(t, ok)
	_, ok = p.TryAcquire(domains.ProviderBale)
	require.False(t, ok, "old active Bale work exceeds reduced quota")
	r()
	for _, r := range releases {
		r()
	}
	empty(t, p)
}
func TestPendingBoundsAndCancellationCleanup(t *testing.T) {
	p := New(1)
	p.SetProviders([]domains.Provider{domains.ProviderBale})
	hold, ok := p.TryAcquire(domains.ProviderBale)
	require.True(t, ok)
	p.mu.Lock()
	var waiters []*waiter
	for connection := 0; connection < 10; connection++ {
		id := string(rune('A' + connection))
		for i := 0; i < MaxPendingPerConnection; i++ {
			w, err := p.enqueueLocked(domains.ProviderBale, id)
			require.NoError(t, err)
			waiters = append(waiters, w)
		}
		_, err := p.enqueueLocked(domains.ProviderBale, id)
		var domain *domains.Error
		require.ErrorAs(t, err, &domain)
		require.Equal(t, "RESOURCE_BUSY", domain.Code)
	}
	_, err := p.enqueueLocked(domains.ProviderBale, "different")
	var domain *domains.Error
	require.ErrorAs(t, err, &domain)
	require.Equal(t, "RESOURCE_BUSY", domain.Code)
	for _, w := range waiters {
		p.removeLocked(w)
	}
	p.mu.Unlock()
	hold()
	empty(t, p)
}
func TestCancellationRacingGrantHasNoOrphanPermit(t *testing.T) {
	p := New(1)
	p.SetProviders([]domains.Provider{domains.ProviderBale})
	for i := 0; i < 200; i++ {
		hold, ok := p.TryAcquire(domains.ProviderBale)
		require.True(t, ok)
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() {
			r, err := p.AcquireFor(ctx, domains.ProviderBale, "race")
			if r != nil {
				r()
			}
			finished <- err
		}()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); hold() }()
		go func() { defer wg.Done(); cancel() }()
		wg.Wait()
		err := <-finished
		require.True(t, err == nil || errors.Is(err, context.Canceled))
		empty(t, p)
	}
}
