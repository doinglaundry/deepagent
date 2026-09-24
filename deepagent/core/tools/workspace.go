package tools

import (
	"fmt"
	"time"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
)

type WorkspaceToolOptions struct {
	ReadOnly       bool
	EnableCommands bool
	EnablePatch    bool
	CommandTimeout time.Duration
}

// NewWorkspaceTools is the only workspace tool factory for local and Docker.
func NewWorkspaceTools(ws backend.ToolWorkspace, opts WorkspaceToolOptions) ([]einotool.BaseTool, error) {
	if ws == nil {
		return nil, fmt.Errorf("workspace is required")
	}
	items := []einotool.BaseTool{
		NewListFilesTool(ws), &ListFilesTool{backend: ws, name: "ls"}, NewReadFileTool(ws),
		&fileSearchTool{backend: ws, name: "glob"}, &fileSearchTool{backend: ws, name: "grep"},
		&fileSearchTool{backend: ws, name: "rg"}, NewSearchFilesTool(ws),
	}
	semantic, err := NewSemanticSearchTool(ws)
	if err != nil {
		return nil, err
	}
	items = append(items, semantic)
	if opts.ReadOnly {
		return items, nil
	}
	items = append(items, NewWriteFileTool(ws), NewEditFileTool(ws), NewDeleteFileTool(ws))
	if opts.EnablePatch {
		items = append(items, NewApplyPatchTool(ws))
	}
	if opts.EnableCommands {
		lints, err := NewReadLintsTool(ws, ws)
		if err != nil {
			return nil, err
		}
		items = append(items, lints)
		items = append(items, NewCommandTools(ws, opts.CommandTimeout)...)
	}
	return items, nil
}
