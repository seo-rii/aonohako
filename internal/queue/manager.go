package queue

import (
	"context"
	"errors"
	"sync"
)

var ErrQueueFull = errors.New("queue_full")

type waitEntry struct {
	ch chan struct{}
}

type Permit struct {
	mgr      *Manager
	entry    *waitEntry
	position int
	once     sync.Once

	mu      sync.Mutex
	granted bool
}

func (p *Permit) Position() int {
	return p.position
}

func (p *Permit) Wait(ctx context.Context) error {
	if p.entry == nil {
		return nil
	}
	select {
	case <-p.entry.ch:
		p.setGranted(true)
		return nil
	case <-ctx.Done():
		if p.mgr.removeWaiter(p.entry) {
			return ctx.Err()
		}
		// Race: permit granted while context was cancelled.
		<-p.entry.ch
		p.setGranted(true)
		p.Release()
		return ctx.Err()
	}
}

func (p *Permit) setGranted(v bool) {
	p.mu.Lock()
	p.granted = v
	p.mu.Unlock()
}

func (p *Permit) isGranted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.granted
}

func (p *Permit) Release() {
	if p == nil || p.mgr == nil || !p.isGranted() {
		return
	}
	p.once.Do(func() {
		p.mgr.release()
	})
}

func (p *Permit) Cancel() {
	if p == nil || p.mgr == nil {
		return
	}
	if p.entry != nil && !p.isGranted() {
		_ = p.mgr.removeWaiter(p.entry)
		return
	}
	p.Release()
}

type Manager struct {
	maxActive  int
	maxPending int

	mu      sync.Mutex
	active  int
	waiters []*waitEntry
}

func New(maxActive, maxPending int) *Manager {
	if maxActive < 1 {
		maxActive = 1
	}
	if maxPending < 0 {
		maxPending = 0
	}
	return &Manager{maxActive: maxActive, maxPending: maxPending}
}

func (m *Manager) Acquire() (*Permit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active < m.maxActive {
		m.active++
		return &Permit{mgr: m, position: 0, granted: true}, nil
	}

	if m.maxPending > 0 && len(m.waiters) >= m.maxPending {
		return nil, ErrQueueFull
	}

	entry := &waitEntry{ch: make(chan struct{})}
	m.waiters = append(m.waiters, entry)
	return &Permit{mgr: m, entry: entry, position: len(m.waiters)}, nil
}

func (m *Manager) removeWaiter(target *waitEntry) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, w := range m.waiters {
		if w == target {
			m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
			return true
		}
	}
	return false
}

func (m *Manager) release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.waiters) > 0 {
		next := m.waiters[0]
		m.waiters = m.waiters[1:]
		close(next.ch)
		return
	}
	if m.active > 0 {
		m.active--
	}
}

func (m *Manager) Snapshot() (active, pending int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active, len(m.waiters)
}
