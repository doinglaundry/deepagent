package local

import (
	"context"
	"fmt"

	"eino-cli/backend/consts"
	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandboxpaths"
)

type SandboxManagerLocal struct {
	sessionID string
	sandbox   *Sandbox
}

// New builds a manager bound to one CLI session; the sandbox is created at startup.
func New(sessionID string) (sandbox.SandboxManager, error) {
	if sessionID == "" {
		return nil, sandbox.ErrSessionIDRequired
	}
	mounts, err := sandboxpaths.BuildMountMappings(sessionID)
	if err != nil {
		return nil, fmt.Errorf("local manager: %w", err)
	}
	sandboxID := consts.LocalSessionIDPrefix + sessionID
	sb := newSandbox(sessionID, sandboxID, mounts)
	return &SandboxManagerLocal{sessionID: sessionID, sandbox: sb}, nil
}

func (m *SandboxManagerLocal) SessionID() string { return m.sessionID }

func (m *SandboxManagerLocal) GetSandboxIdBySessionId(_ context.Context, sessionID string) (string, error) {
	if sessionID != "" && sessionID != m.sessionID {
		return "", fmt.Errorf("local manager: session_id %q does not match %q", sessionID, m.sessionID)
	}
	return m.sandbox.ID(), nil
}

func (m *SandboxManagerLocal) Get(_ context.Context, sandboxID string) (sandbox.Sandbox, error) {
	if sandboxID == "" || m.sandbox == nil || sandboxID != m.sandbox.ID() {
		return nil, sandbox.NewNotFoundError(sandboxID)
	}
	return m.sandbox, nil
}

func (m *SandboxManagerLocal) Release(context.Context, string) error { return nil }

func (m *SandboxManagerLocal) Reset() {}

func (m *SandboxManagerLocal) UsesSessionDataMounts() bool { return true }

func (m *SandboxManagerLocal) AllowsIsolatedExec() bool { return false }
