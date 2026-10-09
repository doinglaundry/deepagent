package sandbox

import (
	"sync"

	agentmodel "eino-cli/deepagent/model"
)

var defaultManager struct {
	sync.RWMutex
	m agentmodel.SandboxManager
}

// Default returns the process-wide manager, or nil when none is registered.
func Default() agentmodel.SandboxManager {
	defaultManager.RLock()
	defer defaultManager.RUnlock()
	return defaultManager.m
}

func SetDefault(m agentmodel.SandboxManager) {
	defaultManager.Lock()
	defer defaultManager.Unlock()
	defaultManager.m = m
}

func ShutdownDefault() {
	defaultManager.Lock()
	m := defaultManager.m
	defaultManager.m = nil
	defaultManager.Unlock()

	s, ok := m.(agentmodel.Shutdowner)
	if ok {
		s.Shutdown()
	}
}
