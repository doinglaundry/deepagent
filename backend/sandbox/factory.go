package sandbox

import (
	"fmt"

	"eino-cli/backend/config"
)

// NewSandboxManager builds the manager selected by cfg.Sandbox.Use.
func NewSandboxManager(cfg *config.Config) (SandboxManager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("sandbox: nil config")
	}
	switch cfg.Sandbox.Use {
	case "", "local":
		if newLocal == nil {
			return nil, fmt.Errorf("sandbox: local manager not registered (init order bug)")
		}
		return newLocal(cfg)
	case "aio":
		if newAio == nil {
			return nil, fmt.Errorf("sandbox: aio manager not registered (init order bug)")
		}
		return newAio(cfg)
	default:
		return nil, fmt.Errorf("sandbox: unknown sandbox.use %q (allowed: local, aio)", cfg.Sandbox.Use)
	}
}

// Indirection so factory.go doesn't import local/ + aio/ (would cycle).
var (
	newLocal func(*config.Config) (SandboxManager, error)
	newAio   func(*config.Config) (SandboxManager, error)
)

// RegisterLocalFactory wires the local provider; called from sandbox/local init().
func RegisterLocalFactory(fn func(*config.Config) (SandboxManager, error)) { newLocal = fn }

// RegisterAioFactory wires the aio provider; called from sandbox/aio init().
func RegisterAioFactory(fn func(*config.Config) (SandboxManager, error)) { newAio = fn }
