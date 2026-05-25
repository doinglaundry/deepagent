package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"eino-cli/backend/config"
	"eino-cli/backend/consts"
	"eino-cli/backend/sandbox"
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
	pathMappings, err := buildPathMappings(sessionID)
	if err != nil {
		return nil, err
	}
	sandboxID := consts.LocalSessionIDPrefix + sessionID
	sb := newSandbox(sessionID, sandboxID, pathMappings)
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

func buildPathMappings(sessionID string) ([]*PathMapping, error) {
	if err := config.EnsureSessionDirs(sessionID); err != nil {
		return nil, fmt.Errorf("local manager: ensure dirs: %w", err)
	}
	var out []*PathMapping
	skillPath := filepath.Join(config.RootDir(), "backend", "skills")
	if info, err := os.Stat(skillPath); err == nil && info.IsDir() {
		if absSkillPath, err := filepath.Abs(skillPath); err == nil {
			out = append(out, &PathMapping{
				ContainerPath: "/mnt/skills",
				LocalPath:     absSkillPath,
				ReadOnly:      true,
			})
		}
	}
	out = append(out,
		&PathMapping{ContainerPath: "/mnt/workspace", LocalPath: config.SandboxWorkDir(sessionID)},
		&PathMapping{ContainerPath: "/mnt/uploads", LocalPath: config.SandboxUploadsDir(sessionID)},
		&PathMapping{ContainerPath: "/mnt/outputs", LocalPath: config.SandboxOutputsDir(sessionID)},
	)
	return out, nil
}
