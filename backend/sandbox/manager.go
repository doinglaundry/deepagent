package sandbox

import (
	"context"
	"sync/atomic"
)

// SandboxManager owns the lifecycle of one or more Sandbox instances:
// Acquire creates / reuses on demand, Get looks up a live sandbox by id,
// Release returns it to a warm pool (or drops it, manager's choice).
//
// UsesThreadDataMounts: tools check this before reverse-mapping
// /mnt/user-data/... paths. Local manager returns true; an aio manager
// that doesn't actually bind-mount per-thread dirs returns false.
type SandboxManager interface {
	Acquire(ctx context.Context, threadID string) (string, error)
	Get(ctx context.Context, sandboxID string) (Sandbox, error)
	Release(ctx context.Context, sandboxID string) error

	Reset()
	UsesThreadDataMounts() bool
}

// defaultManager is the process-wide singleton managers see via Default().
// atomic.Pointer keeps the read path lock-free; SetDefault swaps the value
// atomically so concurrent Acquire() calls during a swap never observe a
// torn pointer.
var defaultManager atomic.Pointer[managerHandle]

// managerHandle: indirection so atomic.Pointer holds a concrete type
// (atomic.Pointer doesn't accept an interface directly without an extra
// indirection in older Go versions and adds an alloc anyway).
type managerHandle struct{ m SandboxManager }

// Default returns the registered manager, or nil if none was set. Callers
// (tool wrappers, middleware) must handle nil — typically by short-circuiting
// the tool into a no-op so plain CLI runs without a sandbox don't crash.
func Default() SandboxManager {
	h := defaultManager.Load()
	if h == nil {
		return nil
	}
	return h.m
}

// SetDefault swaps the active manager. Passing nil clears the registration.
func SetDefault(m SandboxManager) {
	if m == nil {
		defaultManager.Store(nil)
		return
	}
	defaultManager.Store(&managerHandle{m: m})
}

// ResetDefault clears whatever manager is registered. Useful for tests that
// build their own manager per-case.
func ResetDefault() {
	defaultManager.Store(nil)
}

// Shutdown is a hook for managers that want to clean up containers / files
// on app exit. Implementations opt in by satisfying this interface; Default
// callers use a type assertion before invoking.
type Shutdowner interface {
	Shutdown()
}

// ShutdownDefault calls Shutdown() on the active manager if it implements
// Shutdowner, then clears the registration. Safe to call when none is set.
func ShutdownDefault() {
	h := defaultManager.Load()
	if h == nil {
		return
	}
	if s, ok := h.m.(Shutdowner); ok {
		s.Shutdown()
	}
	defaultManager.Store(nil)
}
