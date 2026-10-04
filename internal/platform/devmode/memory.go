package devmode

import (
	"context"
	"slices"
	"sync"
	"time"
)

// MemoryStore is a process-local Store for tests and tools. It keeps the
// audit trail in memory so tests can assert on it.
type MemoryStore struct {
	mu     sync.Mutex
	rec    Record
	tokens map[[32]byte]*memoryToken
	audit  []AuditedEvent
}

// AuditedEvent is one event a Store recorded.
type AuditedEvent struct {
	Actor Actor
	Event Event
	// TokenIssued marks the token issuance record, which has no Event.
	TokenIssued bool
}

type memoryToken struct {
	expires  time.Time
	redeemed bool
}

// NewMemoryStore returns an empty store with an inactive session.
func NewMemoryStore() *MemoryStore { return &MemoryStore{tokens: map[[32]byte]*memoryToken{}} }

func (m *MemoryStore) LoadDevSession(context.Context) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.copyLocked(), nil
}

func (m *MemoryStore) copyLocked() Record {
	r := m.rec
	r.Toggles = slices.Clone(m.rec.Toggles)
	return r
}

func (m *MemoryStore) UpdateDevSession(ctx context.Context, actor Actor, fn Mutation) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, events, err := fn(m.copyLocked(), memoryRedeemer{m})
	if next.Version != m.rec.Version {
		m.rec = next
		m.rec.Toggles = slices.Clone(next.Toggles)
	}
	for _, e := range events {
		m.audit = append(m.audit, AuditedEvent{Actor: actor, Event: e})
	}
	return m.copyLocked(), err
}

func (m *MemoryStore) IssueDevToken(_ context.Context, actor Actor, digest [32]byte, _, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[digest] = &memoryToken{expires: expiresAt}
	m.audit = append(m.audit, AuditedEvent{Actor: actor, TokenIssued: true})
	return nil
}

// Audit returns the recorded events in order.
func (m *MemoryStore) Audit() []AuditedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.audit)
}

// memoryRedeemer runs with the store lock held by UpdateDevSession.
type memoryRedeemer struct{ m *MemoryStore }

func (r memoryRedeemer) RedeemToken(_ context.Context, digest [32]byte, now time.Time) (bool, error) {
	t := r.m.tokens[digest]
	if t == nil || t.redeemed || !now.Before(t.expires) {
		return false, nil
	}
	t.redeemed = true
	return true, nil
}
