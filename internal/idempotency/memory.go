package idempotency

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-process Store. It follows the same rules as the
// PostgreSQL store and is used by tests; it does not survive restarts and is
// not shared between replicas.
type MemoryStore struct {
	mu          sync.Mutex
	records     map[Key]*memoryRecord
	ttl         time.Duration
	lockTimeout time.Duration
	now         func() time.Time
}

type memoryRecord struct {
	fingerprint []byte
	response    *Response
	startedAt   time.Time
	expiresAt   time.Time
}

// NewMemoryStore returns a store whose records live for ttl. A request that
// stays in progress longer than lockTimeout is considered abandoned.
func NewMemoryStore(ttl, lockTimeout time.Duration, now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{records: make(map[Key]*memoryRecord), ttl: ttl, lockTimeout: lockTimeout, now: now}
}

// Begin implements Store.
func (m *MemoryStore) Begin(_ context.Context, key Key, fingerprint []byte) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	rec, ok := m.records[key]
	abandoned := ok && rec.response == nil && now.Sub(rec.startedAt) > m.lockTimeout
	if !ok || now.After(rec.expiresAt) || abandoned {
		m.records[key] = &memoryRecord{
			fingerprint: append([]byte(nil), fingerprint...),
			startedAt:   now,
			expiresAt:   now.Add(m.ttl),
		}
		return Result{Outcome: Started}, nil
	}

	switch {
	case !bytes.Equal(rec.fingerprint, fingerprint):
		return Result{Outcome: Mismatch}, nil
	case rec.response == nil:
		return Result{Outcome: InProgress}, nil
	default:
		resp := *rec.response
		return Result{Outcome: Replay, Response: &resp}, nil
	}
}

// Complete implements Store.
func (m *MemoryStore) Complete(_ context.Context, key Key, resp Response) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.records[key]; ok {
		resp.Body = append([]byte(nil), resp.Body...)
		resp.Header = resp.Header.Clone()
		rec.response = &resp
	}
	return nil
}

// Release implements Store.
func (m *MemoryStore) Release(_ context.Context, key Key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key)
	return nil
}
