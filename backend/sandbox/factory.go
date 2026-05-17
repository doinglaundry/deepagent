package sandbox

import (
	"fmt"

	"eino-cli/backend/config"
)

// NewSandboxManager picks a concrete manager from cfg.Sandbox.Use. Explicit
// switch — not reflection / init-time registration — keeps the package
// dependency tree visible from one place. Adding a new manager is +3 lines.
//
// This function is wired in M1's NewLocalManager call; the aio variant
// follows in M3. We keep them behind a local indirection variable so this
// file doesn't import the aio package directly until M3 lands (avoids a
// false "import cycle for unused thing" during the M1→M3 transition).
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
			return nil, fmt.Errorf("sandbox: aio manager not yet implemented (set sandbox.use=local)")
		}
		return newAio(cfg)
	default:
		return nil, fmt.Errorf("sandbox: unknown sandbox.use %q (allowed: local, aio)", cfg.Sandbox.Use)
	}
}

// newLocal / newAio are wired by the respective sub-packages via an init()
// in a thin registration file. Keeps factory.go from importing local/ +
// aio/ directly — those packages need to import the top-level sandbox
// package for the Sandbox/SandboxManager interfaces, so the reverse import
// would cycle.
var (
	newLocal func(*config.Config) (SandboxManager, error)
	newAio   func(*config.Config) (SandboxManager, error)
)

// RegisterLocalFactory is called by sandbox/local/init.go. Tests can stub.
func RegisterLocalFactory(fn func(*config.Config) (SandboxManager, error)) {
	newLocal = fn
}

// RegisterAioFactory: same for sandbox/aio (M3).
func RegisterAioFactory(fn func(*config.Config) (SandboxManager, error)) {
	newAio = fn
}
