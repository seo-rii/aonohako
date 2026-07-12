package queue

import (
	"context"
	"errors"
	"sync"
)

var ErrQueueFull = errors.New("queue_full")
var ErrPermitCanceled = errors.New("permit_canceled")

type permitState uint8

const (
	permitPending permitState = iota
	permitGranted
	permitAcquired
	permitCanceled
	permitReleased
)

type waitEntry struct {
	ch     chan struct{}
	permit *Permit
}

type Permit struct {
	mgr      *Manager
	entry    *waitEntry
	position int
	once     sync.Once

	mu    sync.Mutex
	state permitState
}

func (p *Permit) Position() int {
	return p.position
}

func (p *Permit) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		p.Cancel()
		return err
	}
	if p.entry == nil {
		p.mu.Lock()
		acquired := p.state == permitAcquired
		p.mu.Unlock()
		if !acquired {
			return ErrPermitCanceled
		}
		if err := ctx.Err(); err != nil {
			p.Cancel()
			return err
		}
		return nil
	}
	select {
	case <-p.entry.ch:
		if err := ctx.Err(); err != nil {
			p.Cancel()
			return err
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		switch p.state {
		case permitGranted:
			p.state = permitAcquired
			return nil
		case permitAcquired:
			return nil
		default:
			return ErrPermitCanceled
		}
	case <-ctx.Done():
		p.Cancel()
		return ctx.Err()
	}
}

func (p *Permit) Release() {
	if p == nil || p.mgr == nil {
		return
	}
	p.mu.Lock()
	shouldRelease := p.state == permitGranted || p.state == permitAcquired
	if shouldRelease {
		p.state = permitReleased
	}
	p.mu.Unlock()
	if shouldRelease {
		p.once.Do(func() {
			p.mgr.release()
		})
	}
}

func (p *Permit) Cancel() {
	if p == nil || p.mgr == nil {
		return
	}
	if p.mgr.cancel(p) {
		p.once.Do(func() {
			p.mgr.release()
		})
	}
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
		return &Permit{mgr: m, position: 0, state: permitAcquired}, nil
	}

	if m.maxPending > 0 && len(m.waiters) >= m.maxPending {
		return nil, ErrQueueFull
	}

	entry := &waitEntry{ch: make(chan struct{})}
	permit := &Permit{mgr: m, entry: entry, position: len(m.waiters) + 1, state: permitPending}
	entry.permit = permit
	m.waiters = append(m.waiters, entry)
	return permit, nil
}

func (m *Manager) cancel(permit *Permit) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	permit.mu.Lock()
	defer permit.mu.Unlock()
	switch permit.state {
	case permitPending:
		for i, waiter := range m.waiters {
			if waiter.permit == permit {
				m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
				break
			}
		}
		permit.state = permitCanceled
		close(permit.entry.ch)
		return false
	case permitGranted, permitAcquired:
		permit.state = permitCanceled
		return true
	default:
		return false
	}
}

func (m *Manager) release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.waiters) > 0 {
		next := m.waiters[0]
		m.waiters = m.waiters[1:]
		next.permit.mu.Lock()
		if next.permit.state != permitPending {
			next.permit.mu.Unlock()
			continue
		}
		next.permit.state = permitGranted
		close(next.ch)
		next.permit.mu.Unlock()
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
