package runnerhub

import (
	"context"
	"sync"
)

// memoryStore is the default FinalizedStore: finalized ids do not survive a
// backend restart.
type memoryStore struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newMemoryStore() *memoryStore {
	return &memoryStore{seen: make(map[string]struct{})}
}

func (s *memoryStore) MarkFinalized(_ context.Context, runID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[runID]; ok {
		return false, nil
	}
	s.seen[runID] = struct{}{}
	return true, nil
}

func (s *memoryStore) IsFinalized(_ context.Context, runID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[runID]
	return ok, nil
}
