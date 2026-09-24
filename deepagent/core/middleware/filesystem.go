package middleware

import (
	"context"
	"fmt"
	"time"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type FilesystemConfig struct {
	Workspace                                   backend.ToolWorkspace
	OwnWorkspace                                bool
	ReadOnly, DisableExecute, DisableApplyPatch bool
	CommandTimeout                              time.Duration
}

type filesystemMiddleware struct {
	BaseMiddleware
	cfg *FilesystemConfig
}

func NewFilesystem(cfg *FilesystemConfig) Middleware { return &filesystemMiddleware{cfg: cfg} }
func (m *filesystemMiddleware) Name() string         { return "filesystem" }
func (m *filesystemMiddleware) BuildPrompt(context.Context) ([]*schema.Message, error) {
	if m == nil || m.cfg == nil || m.cfg.Workspace == nil {
		return nil, fmt.Errorf("workspace is required")
	}
	return []*schema.Message{schema.SystemMessage("Use workspace tools to inspect and edit files. Paths are scoped to the configured workspace. Read file offsets are one-based line numbers.")}, nil
}
func (m *filesystemMiddleware) Tools(context.Context) ([]tool.BaseTool, error) {
	if m == nil || m.cfg == nil {
		return nil, fmt.Errorf("filesystem config is required")
	}
	return tools.NewWorkspaceTools(m.cfg.Workspace, tools.WorkspaceToolOptions{
		ReadOnly:       m.cfg.ReadOnly,
		EnableCommands: !m.cfg.DisableExecute,
		EnablePatch:    !m.cfg.DisableApplyPatch,
		CommandTimeout: m.cfg.CommandTimeout,
	})
}
func (m *filesystemMiddleware) NewRun() Middleware {
	if m == nil || m.cfg == nil {
		return NewFilesystem(nil)
	}
	cfg := *m.cfg
	return NewFilesystem(&cfg)
}

func (m *filesystemMiddleware) Close(ctx context.Context) error {
	if m == nil || m.cfg == nil || !m.cfg.OwnWorkspace || m.cfg.Workspace == nil {
		return nil
	}
	return m.cfg.Workspace.Close(ctx)
}
