package manager

import (
	"context"
	"errors"
	"sync"
)

type MemoryStore struct {
	mu      sync.RWMutex
	threads map[string]*Thread
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{threads: make(map[string]*Thread)} }
func (s *MemoryStore) CreateThread(_ context.Context, t *Thread) error {
	if s == nil || t == nil {
		return errors.New("invalid thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[t.ID]; ok {
		return errors.New("thread already exists")
	}
	cp := *t
	s.threads[t.ID] = &cp
	return nil
}
func (s *MemoryStore) GetThread(_ context.Context, id string) (*Thread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.threads[id]
	if !ok {
		return nil, errors.New("thread not found")
	}
	cp := *t
	cp.Inputs = append([][]byte(nil), t.Inputs...)
	return &cp, nil
}
func (s *MemoryStore) ListThreads(_ context.Context, sessionID string) ([]*Thread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Thread, 0)
	for _, t := range s.threads {
		if sessionID == "" || t.SessionID == sessionID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (s *MemoryStore) UpdateThread(_ context.Context, t *Thread) error {
	if t == nil {
		return errors.New("invalid thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[t.ID]; !ok {
		return errors.New("thread not found")
	}
	cp := *t
	s.threads[t.ID] = &cp
	return nil
}

type MemoryQueue struct {
	mu       sync.Mutex
	pending  map[string][][]byte
	canceled map[string]bool
}

func NewMemoryQueue() *MemoryQueue {
	return &MemoryQueue{pending: make(map[string][][]byte), canceled: make(map[string]bool)}
}
func (q *MemoryQueue) Enqueue(_ context.Context, id string, input []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.canceled[id] {
		return errors.New("thread canceled")
	}
	q.pending[id] = append(q.pending[id], append([]byte(nil), input...))
	return nil
}
func (q *MemoryQueue) Has(_ context.Context, id string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending[id]) > 0, nil
}
func (q *MemoryQueue) Cancel(_ context.Context, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.canceled[id] = true
	delete(q.pending, id)
	return nil
}
