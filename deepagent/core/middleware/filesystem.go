package middleware

import (
	"context"
	"eino-cli/deepagent/core/types"
	"fmt"
	"github.com/google/uuid"
	"sync"
	"time"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type FilesystemConfig struct {
	ThreadID                                                           string
	Backend                                                            backend.Backend
	ReadOnly, DisableUploadDownload, DisableExecute, DisableApplyPatch bool
	WorkDir                                                            string
	CommandTimeout                                                     time.Duration
	ToolMask                                                           tools.Mask
}

type filesystemMiddleware struct {
	mu       sync.Mutex
	commands *backend.Commands
	BaseMiddleware
	cfg *FilesystemConfig
}

func NewFilesystem(cfg *FilesystemConfig) Middleware { return &filesystemMiddleware{cfg: cfg} }
func (m *filesystemMiddleware) Name() string         { return "filesystem" }

func (m *filesystemMiddleware) BuildPrompt(ctx context.Context) ([]*schema.Message, error) {
	built, err := m.build(ctx)
	if err != nil || built.AdditionalInstruction == "" {
		return nil, err
	}
	return []*schema.Message{schema.SystemMessage(built.AdditionalInstruction)}, nil
}

func (m *filesystemMiddleware) Tools(ctx context.Context) ([]tool.BaseTool, error) {
	built, err := m.build(ctx)
	if err != nil {
		return nil, err
	}
	return built.AdditionalTools, nil
}

func (m *filesystemMiddleware) build(ctx context.Context) (result struct {
	AdditionalInstruction string
	AdditionalTools       []tool.BaseTool
}, err error) {
	if m == nil || m.cfg == nil || m.cfg.Backend == nil {
		return result, fmt.Errorf("filesystem backend is required")
	}
	result.AdditionalInstruction = "Use workspace file tools to inspect and edit files. Paths are scoped to the configured workspace. Read file offsets are one-based line numbers."
	result.AdditionalTools = tools.NewFilesystemTools(m.cfg.Backend, m.cfg.ReadOnly)
	if workspace, ok := m.cfg.Backend.(backend.Workspace); ok {
		semantic, err := tools.NewSemanticSearchTool(workspace)
		if err != nil {
			return result, err
		}
		result.AdditionalTools = append(result.AdditionalTools, semantic)
	}
	if workspace, ok := m.cfg.Backend.(backend.WorkspaceBackend); ok {
		if !m.cfg.ReadOnly {
			if !m.cfg.DisableExecute {
				if rooted, ok := m.cfg.Backend.(backend.Workspace); ok {
					lints, err := tools.NewReadLintsTool(rooted, m.commandService(workspace))
					if err != nil {
						return result, err
					}
					result.AdditionalTools = append(result.AdditionalTools, lints)
				}
				result.AdditionalTools = append(result.AdditionalTools, tools.NewCommandTools(classifiedCommands{CommandService: m.commandService(workspace)}, m.cfg.CommandTimeout)...)
			}
		}
	}
	if !m.cfg.ReadOnly && !m.cfg.DisableApplyPatch {
		if patcher, ok := m.cfg.Backend.(backend.ApplyPatchBackend); ok && patcher.SupportsApplyPatch() {
			result.AdditionalTools = append(result.AdditionalTools, tools.NewApplyPatchTool(patcher))
		}
	}
	return result, nil
}

func (m *filesystemMiddleware) commandService(workspace backend.WorkspaceBackend) *backend.Commands {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.commands == nil {
		scope := m.cfg.ThreadID
		if scope == "" {
			scope = uuid.NewString()
		}
		rooted, ok := workspace.(backend.Workspace)
		if !ok {
			rooted = backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: workspace.RootDir(), VirtualMode: true})
		}
		m.commands = backend.NewCommands(scope, rooted)
	}
	return m.commands
}
func (m *filesystemMiddleware) BeforeRun(context.Context, *types.RunState) error { return nil }
func (m *filesystemMiddleware) AfterRun(ctx context.Context, _ *types.RunState, _ error) error {
	return m.Close(ctx)
}
func (m *filesystemMiddleware) Close(ctx context.Context) error {
	m.mu.Lock()
	commands := m.commands
	m.commands = nil
	m.mu.Unlock()
	if commands != nil {
		return commands.Close(context.WithoutCancel(ctx))
	}
	return nil
}
func (m *filesystemMiddleware) NewRun() Middleware { config := *m.cfg; return NewFilesystem(&config) }
