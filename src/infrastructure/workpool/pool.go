// Package workpool bounds gateway work across providers and connections.
// Quotas reserve capacity for other enabled providers even when one stalls.
package workpool

import (
	"context"
	"sync"

	"github.com/mimalef70/goomni/src/domains"
)

const (
	MaxPending              = 1000
	MaxPendingPerConnection = 100
)

type waiter struct {
	provider   domains.Provider
	connection string
	done       chan struct{}
	state      uint8 // 0 queued, 1 granted, 2 cancelled/removed
	err        error
	release    func()
}
type connectionQueue struct {
	id      string
	waiters []*waiter
}
type providerState struct {
	active int
	queues map[string]*connectionQueue
	order  []*connectionQueue
	cursor int
}

// Pool preserves a global bound and round-robin fairness between providers and
// between each provider's waiting connections. Within a connection, work is FIFO.
// Call SetProviders before accepting work; an empty list enables no provider.
type Pool struct {
	mu        sync.Mutex
	capacity  int
	active    int
	pending   int
	providers []domains.Provider
	states    map[domains.Provider]*providerState
	cursor    int
}

func New(capacity int) *Pool {
	if capacity < 1 {
		panic("workpool: capacity must be positive")
	}
	return &Pool{capacity: capacity, states: make(map[domains.Provider]*providerState)}
}

// SetProviders atomically replaces enabled providers. Active work is never
// preempted; reduced quotas prevent new grants until existing permits drain.
// Queued work for a removed provider fails rather than waiting indefinitely.
func (p *Pool) SetProviders(providers []domains.Provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := make(map[domains.Provider]bool, len(providers))
	next := make([]domains.Provider, 0, len(providers))
	for _, provider := range providers {
		if provider.Validate() != nil || seen[provider] {
			continue
		}
		seen[provider] = true
		next = append(next, provider)
		if p.states[provider] == nil {
			p.states[provider] = &providerState{queues: make(map[string]*connectionQueue)}
		}
	}
	unchanged := len(next) == len(p.providers)
	for i := range next {
		if !unchanged || next[i] != p.providers[i] {
			unchanged = false
			break
		}
	}
	if unchanged {
		return
	}
	for provider, state := range p.states {
		if seen[provider] {
			continue
		}
		for _, queue := range state.order {
			for _, w := range queue.waiters {
				p.pending--
				w.state = 2
				w.err = domains.Unsupported(string(provider))
				close(w.done)
			}
		}
		state.order = nil
		state.queues = make(map[string]*connectionQueue)
		state.cursor = 0
		if state.active == 0 {
			delete(p.states, provider)
		}
	}
	p.providers = next
	p.cursor = 0
	p.dispatchLocked()
}

func (p *Pool) Acquire(ctx context.Context, provider domains.Provider) (func(), error) {
	return p.AcquireFor(ctx, provider, "")
}
func (p *Pool) AcquireFor(ctx context.Context, provider domains.Provider, connection string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	w, err := p.enqueueLocked(provider, connection)
	if err == nil {
		p.dispatchLocked()
	}
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-w.done:
		// If cancellation raced with a grant, return its permit immediately.
		if err := ctx.Err(); err != nil {
			p.cancel(w)
			return nil, err
		}
		return w.release, w.err
	case <-ctx.Done():
		p.cancel(w)
		return nil, ctx.Err()
	}
}
func (p *Pool) TryAcquire(provider domains.Provider) (func(), bool) {
	return p.TryAcquireFor(provider, "")
}
func (p *Pool) TryAcquireFor(provider domains.Provider, connection string) (func(), bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, err := p.enqueueLocked(provider, connection)
	if err != nil {
		return nil, false
	}
	p.dispatchLocked()
	if w.state == 1 {
		return w.release, true
	}
	p.removeLocked(w)
	return nil, false
}
func (p *Pool) enabledLocked(provider domains.Provider) bool {
	for _, v := range p.providers {
		if v == provider {
			return true
		}
	}
	return false
}
func (p *Pool) quotaLocked() int {
	if len(p.providers) == 0 {
		return 0
	}
	quota := p.capacity / len(p.providers)
	if quota < 1 {
		quota = 1
	}
	return quota
}
func (p *Pool) enqueueLocked(provider domains.Provider, connection string) (*waiter, error) {
	if !p.enabledLocked(provider) {
		return nil, domains.Unsupported(string(provider))
	}
	state := p.states[provider]
	queue := state.queues[connection]
	if p.pending >= MaxPending || (queue != nil && len(queue.waiters) >= MaxPendingPerConnection) {
		return nil, domains.E("RESOURCE_BUSY", "provider work queue is full", 429)
	}
	if queue == nil {
		queue = &connectionQueue{id: connection}
		state.queues[connection] = queue
		state.order = append(state.order, queue)
	}
	w := &waiter{provider: provider, connection: connection, done: make(chan struct{})}
	queue.waiters = append(queue.waiters, w)
	p.pending++
	return w, nil
}
func (p *Pool) dispatchLocked() {
	quota := p.quotaLocked()
	for p.active < p.capacity && p.pending > 0 && len(p.providers) > 0 {
		granted := false
		for offset := 0; offset < len(p.providers); offset++ {
			index := (p.cursor + offset) % len(p.providers)
			provider := p.providers[index]
			state := p.states[provider]
			if state.active >= quota || len(state.order) == 0 {
				continue
			}
			queue := state.order[state.cursor]
			w := queue.waiters[0]
			p.removeLocked(w)
			// A nonempty queue rotates behind the provider's other connections.
			if state.queues[queue.id] != nil && len(state.order) > 0 {
				state.cursor = (state.cursor + 1) % len(state.order)
			}
			p.active++
			state.active++
			w.state = 1
			var once sync.Once
			w.release = func() { once.Do(func() { p.release(provider) }) }
			close(w.done)
			p.cursor = (index + 1) % len(p.providers)
			granted = true
			break
		}
		if !granted {
			return
		}
	}
}

// removeLocked unlinks a pending waiter and drops empty connection queues so
// connection IDs do not accumulate in a long-running gateway.
func (p *Pool) removeLocked(w *waiter) {
	if w.state != 0 {
		return
	}
	state := p.states[w.provider]
	queue := state.queues[w.connection]
	for i, candidate := range queue.waiters {
		if candidate == w {
			queue.waiters = append(queue.waiters[:i], queue.waiters[i+1:]...)
			p.pending--
			break
		}
	}
	w.state = 2
	if len(queue.waiters) > 0 {
		return
	}
	delete(state.queues, queue.id)
	for i, candidate := range state.order {
		if candidate == queue {
			state.order = append(state.order[:i], state.order[i+1:]...)
			if i < state.cursor {
				state.cursor--
			}
			if len(state.order) == 0 || state.cursor >= len(state.order) {
				state.cursor = 0
			}
			break
		}
	}
}
func (p *Pool) cancel(w *waiter) {
	p.mu.Lock()
	if w.state == 0 {
		p.removeLocked(w)
		p.dispatchLocked()
	}
	release := w.release
	p.mu.Unlock()
	if release != nil {
		release()
	}
}
func (p *Pool) release(provider domains.Provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.states[provider]
	p.active--
	state.active--
	if state.active == 0 && !p.enabledLocked(provider) {
		delete(p.states, provider)
	}
	p.dispatchLocked()
}

// Active reports the number of held permits without account or provider labels.
func (p *Pool) Active() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}
