package sandbox

import (
	"context"
	"sync/atomic"
)

// SandboxManager owns Sandbox instances. UsesThreadDataMounts tells tools
// whether to reverse-map /mnt/user-data/... to a per-thread host dir.
type SandboxManager interface {
	Acquire(ctx context.Context, threadID string) (string, error)
	Get(ctx context.Context, sandboxID string) (Sandbox, error)
	Release(ctx context.Context, sandboxID string) error

	Reset()
	UsesThreadDataMounts() bool
}

// Wrapped in a struct so atomic.Pointer holds a concrete (non-interface) type.
var defaultManager atomic.Pointer[managerHandle]

type managerHandle struct{ m SandboxManager }

// Default returns the process-wide manager, or nil when none is registered.
func Default() SandboxManager {
	h := defaultManager.Load()
	if h == nil {
		return nil
	}
	return h.m
}

// SetDefault swaps the active manager; passing nil clears it.
func SetDefault(m SandboxManager) {
	if m == nil {
		defaultManager.Store(nil)
		return
	}
	defaultManager.Store(&managerHandle{m: m})
}

// Shutdowner is the optional teardown hook managers may implement.
type Shutdowner interface{ Shutdown() }

// ShutdownDefault invokes Shutdown on the registered manager when it
// implements Shutdowner, then clears the registration.
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
