package sandbox

import (
	"context"
	"sync"
)

type SandboxManager interface {
	Acquire(ctx context.Context, sessionID string) (string, error)
	Get(ctx context.Context, sandboxID string) (Sandbox, error)
	Release(ctx context.Context, sandboxID string) error

	Reset()
	UsesSessionDataMounts() bool
	AllowsIsolatedExec() bool
}

var defaultManager struct {
	sync.RWMutex
	m SandboxManager
}

// Default returns the process-wide manager, or nil when none is registered.
func Default() SandboxManager {
	defaultManager.RLock()
	defer defaultManager.RUnlock()
	return defaultManager.m
}

func SetDefault(m SandboxManager) {
	defaultManager.Lock()
	defer defaultManager.Unlock()
	defaultManager.m = m
}

type Shutdowner interface{ Shutdown() }

func ShutdownDefault() {
	defaultManager.Lock()
	m := defaultManager.m
	defaultManager.m = nil
	defaultManager.Unlock()

	if s, ok := m.(Shutdowner); ok {
		s.Shutdown()
	}
}
