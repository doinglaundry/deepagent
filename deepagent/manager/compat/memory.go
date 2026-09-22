package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
)

// Memory is an explicitly selected, isolated Manager for tests and embedded use.
// Production New never falls back to it when shared storage is unavailable.
type Memory struct{ *engine }

var _ api.Manager = (*Memory)(nil)

func NewMemory(namespace string) *Memory {
	s := &memoryStore{threads: map[string]*record{}, checkpoints: map[string][]byte{}, subscriptions: map[*memorySubscription]struct{}{}}
	return &Memory{&engine{namespace: namespace, store: s}}
}

type memorySubscription struct {
	session string
	events  chan protocol.Event
	errors  chan error
	done    chan struct{}
}
type memoryStore struct {
	jobs          map[string]memoryJob
	mu            sync.Mutex
	threads       map[string]*record
	checkpoints   map[string][]byte
	log           []protocol.Event
	sequence      int64
	subscriptions map[*memorySubscription]struct{}
	closed        bool
}

func (*memoryStore) enqueueInput(context.Context, string) error  { return nil }
func (*memoryStore) completeInput(context.Context, string) error { return nil }
func (*memoryStore) requeueInput(context.Context, string) error  { return nil }

func cloneRecord(r *record) *record {
	b, _ := json.Marshal(r)
	var out record
	_ = json.Unmarshal(b, &out)
	out.now = time.Now().UTC()
	return &out
}
func (s *memoryStore) check(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if s.closed {
		return fmt.Errorf("manager closed")
	}
	return nil
}
func (s *memoryStore) create(ctx context.Context, r *record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return e
	}
	if _, ok := s.threads[r.Thread.ID]; ok {
		return api.ErrConflict
	}
	r.Revision = 1
	s.threads[r.Thread.ID] = cloneRecord(r)
	return nil
}
func (s *memoryStore) load(ctx context.Context, id string) (*record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	r, ok := s.threads[id]
	if !ok {
		return nil, api.ErrNotFound
	}
	return cloneRecord(r), nil
}
func (s *memoryStore) update(ctx context.Context, id string, fn func(*record) error) (*record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	old, ok := s.threads[id]
	if !ok {
		return nil, api.ErrNotFound
	}
	r := cloneRecord(old)
	if e := fn(r); e != nil {
		return nil, e
	}

	log := append([]protocol.Event(nil), s.log...)
	sequence := s.sequence
	for _, event := range recordEvents(r) {
		if !event.Durable() {
			continue
		}
		found := false
		for _, stored := range log {
			if stored.ID == event.ID {
				if stored.ThreadID != id || !sameEvent(stored, *event) {
					return nil, api.ErrConflict
				}
				*event = stored
				found = true
				break
			}
		}
		if !found {
			sequence++
			event.Sequence = sequence
			log = append(log, cloneEvent(*event))
		}
	}
	s.log = log
	s.sequence = sequence

	r.Revision++
	r.Thread.UpdatedAt = r.now
	s.threads[id] = cloneRecord(r)
	return cloneRecord(r), nil
}
func cloneEvent(e protocol.Event) protocol.Event {
	b, _ := json.Marshal(e)
	var out protocol.Event
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *memoryStore) list(ctx context.Context, session string, runnable bool, limit, offset int) ([]api.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	out := []api.Thread{}
	now := time.Now()
	for _, r := range s.threads {
		if session != "" && r.Thread.SessionID != session {
			continue
		}
		if runnable {
			if r.Thread.State != api.Ready && r.Thread.State != api.Running && r.Thread.State != api.Closing {
				continue
			}
			if r.Permit.Token != "" && r.Permit.ExpiresAt.After(now) {
				continue
			}
		}
		out = append(out, cloneRecord(r).Thread)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	limit, offset = bounds(limit, offset)
	if offset >= len(out) {
		return []api.Thread{}, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}
func (s *memoryStore) events(ctx context.Context, f api.EventFilter) ([]protocol.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	limit, _ := bounds(f.Limit, 0)
	out := []protocol.Event{}
	for _, e := range s.log {
		if e.Sequence <= f.After || (f.ThreadID != "" && e.ThreadID != f.ThreadID) || (f.SessionID != "" && e.SessionID != f.SessionID) {
			continue
		}
		out = append(out, cloneEvent(e))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
func (s *memoryStore) deliver(ctx context.Context, r *record) ([]protocol.Input, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return pending(r), nil
}
func (s *memoryStore) publish(ctx context.Context, e protocol.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx); err != nil {
		return err
	}
	for sub := range s.subscriptions {
		if sub.session != e.SessionID {
			continue
		}
		select {
		case sub.events <- cloneEvent(e):
		default:
			select {
			case sub.errors <- fmt.Errorf("subscription overflow; reconcile durable events"):
			default:
			}
		}
	}
	return nil
}
func (s *memoryStore) subscribe(ctx context.Context, session string) (*api.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	sub := &memorySubscription{session: session, events: make(chan protocol.Event, 128), errors: make(chan error, 1), done: make(chan struct{})}
	s.subscriptions[sub] = struct{}{}
	closeFn := func() { s.mu.Lock(); defer s.mu.Unlock(); s.remove(sub) }
	go func() {
		select {
		case <-ctx.Done():
			closeFn()
		case <-sub.done:
		}
	}()
	return &api.Subscription{Events: sub.events, Errors: sub.errors, Close: closeFn}, nil
}
func (s *memoryStore) remove(sub *memorySubscription) {
	if _, ok := s.subscriptions[sub]; ok {
		delete(s.subscriptions, sub)
		close(sub.done)
		close(sub.events)
		close(sub.errors)
	}
}
func (s *memoryStore) checkpoint(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	b, ok := s.checkpoints[key]
	if !ok {
		return nil, api.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}
func (s *memoryStore) putCheckpoint(ctx context.Context, key string, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return e
	}
	s.checkpoints[key] = append([]byte(nil), b...)
	return nil
}
func (s *memoryStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for sub := range s.subscriptions {
		s.remove(sub)
	}
	return nil
}
