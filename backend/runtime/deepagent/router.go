package deepagent

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"eino-cli/backend/config"
	rt "eino-cli/backend/runtime"
)

const (
	defaultMaxThreads  = 64
	defaultIdleTimeout = 30 * time.Minute
	idleSweepInterval  = 5 * time.Minute
)

// Router maps thread_id → Runtime with LRU bound + idle sweeper.
type Router struct {
	cfg *config.Config

	mu       sync.Mutex
	cache    map[string]*list.Element
	order    *list.List
	maxKept  int
	idleTTL  time.Duration
	stopIdle chan struct{}
	shutdown sync.Once
}

type threadEntry struct {
	tid     string
	runtime rt.Runtime
	last    time.Time
}

func NewRouter(cfg *config.Config) *Router {
	r := &Router{
		cfg:      cfg,
		cache:    map[string]*list.Element{},
		order:    list.New(),
		maxKept:  defaultMaxThreads,
		idleTTL:  defaultIdleTimeout,
		stopIdle: make(chan struct{}),
	}
	go r.idleLoop()
	return r
}

func (r *Router) Get(ctx context.Context, tid string) (rt.Runtime, error) {
	if tid == "" {
		return nil, fmt.Errorf("router: thread_id required")
	}
	r.mu.Lock()
	if el, ok := r.cache[tid]; ok {
		r.order.MoveToFront(el)
		entry := el.Value.(*threadEntry)
		entry.last = time.Now()
		r.mu.Unlock()
		return entry.runtime, nil
	}
	r.mu.Unlock()

	runtime, err := NewRuntime(ctx, r.cfg)
	if err != nil {
		return nil, fmt.Errorf("router: build runtime for %s: %w", tid, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.cache[tid]; ok {
		r.order.MoveToFront(el)
		return el.Value.(*threadEntry).runtime, nil
	}
	entry := &threadEntry{tid: tid, runtime: runtime, last: time.Now()}
	el := r.order.PushFront(entry)
	r.cache[tid] = el
	r.evictLocked()
	return runtime, nil
}

func (r *Router) evictLocked() {
	for r.order.Len() > r.maxKept {
		el := r.order.Back()
		if el == nil {
			return
		}
		entry := el.Value.(*threadEntry)
		delete(r.cache, entry.tid)
		r.order.Remove(el)
	}
}

func (r *Router) idleLoop() {
	t := time.NewTicker(idleSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-r.stopIdle:
			return
		case <-t.C:
			r.sweepIdle()
		}
	}
}

func (r *Router) sweepIdle() {
	cutoff := time.Now().Add(-r.idleTTL)
	r.mu.Lock()
	defer r.mu.Unlock()
	for tid, el := range r.cache {
		entry := el.Value.(*threadEntry)
		if entry.last.Before(cutoff) {
			r.order.Remove(el)
			delete(r.cache, tid)
		}
	}
}

func (r *Router) Shutdown() {
	r.shutdown.Do(func() { close(r.stopIdle) })
}
