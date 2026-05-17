package eino

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"eino-cli/backend/config"
)

const (
	defaultMaxThreads      = 64
	defaultIdleTimeout     = 30 * time.Minute
	idleSweepInterval      = 5 * time.Minute
)

// Router maps thread_id → DeepAgentRuntime with an LRU bound and an
// idle-timeout sweeper. The gateway calls Router.Get per HTTP request;
// the CLI mode wraps one Router around its single tid.
type Router struct {
	cfg *config.Config

	mu       sync.Mutex
	cache    map[string]*list.Element // tid -> *list.Element wrapping *threadEntry
	order    *list.List
	maxKept  int
	idleTTL  time.Duration
	stopIdle chan struct{}
	shutdown sync.Once
}

type threadEntry struct {
	tid     string
	runtime Runtime
	last    time.Time
}

// NewRouter constructs a router. ctx is intentionally NOT a field — the
// idle goroutine uses stopIdle so Shutdown gives us a clean teardown
// signal without a "ctx leaked into a struct" smell.
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

// Get returns the runtime for tid, building it lazily on first call.
// The LRU bump keeps active threads alive across the sweep window.
func (r *Router) Get(ctx context.Context, tid string) (Runtime, error) {
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

	// Build outside the lock — NewDeepAgentRuntime calls eino which can
	// take hundreds of ms on first build; holding the cache lock would
	// stall every other thread's Get.
	rt, err := NewDeepAgentRuntime(ctx, r.cfg)
	if err != nil {
		return nil, fmt.Errorf("router: build runtime for %s: %w", tid, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.cache[tid]; ok {
		r.order.MoveToFront(el)
		return el.Value.(*threadEntry).runtime, nil
	}
	entry := &threadEntry{tid: tid, runtime: rt, last: time.Now()}
	el := r.order.PushFront(entry)
	r.cache[tid] = el
	r.evictLocked()
	return rt, nil
}

// Drop releases tid eagerly — gateway calls it after long-running threads
// close (logout, /clear-history with --forget=true).
func (r *Router) Drop(tid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.cache[tid]; ok {
		r.order.Remove(el)
		delete(r.cache, tid)
	}
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

// Shutdown stops the idle sweeper. Idempotent so the gateway and the CLI
// can both `defer router.Shutdown()` without coordinating.
func (r *Router) Shutdown() {
	r.shutdown.Do(func() { close(r.stopIdle) })
}
