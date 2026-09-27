package tools

import (
	"fmt"
	"time"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
)

type FilesystemToolOptions struct {
	ReadOnly       bool
	EnableCommands bool
	EnablePatch    bool
	CommandTimeout time.Duration
}

// NewFilesystemTools is the only filesystem tool factory for local and Docker.
func NewFilesystemTools(filesystem backend.ToolFilesystem, opts FilesystemToolOptions) ([]einotool.BaseTool, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("filesystem is required")
	}
	items := []einotool.BaseTool{
		NewListFilesTool(filesystem), NewReadFileTool(filesystem),
		&fileSearchTool{backend: filesystem, name: "glob"}, &fileSearchTool{backend: filesystem, name: "grep"},
		&fileSearchTool{backend: filesystem, name: "rg"},
	}
	semantic, err := NewSemanticSearchTool(filesystem)
	if err != nil {
		return nil, err
	}
	items = append(items, semantic)
	if opts.ReadOnly {
		return items, nil
	}
	items = append(items, NewWriteFileTool(filesystem), NewEditFileTool(filesystem), NewDeleteFileTool(filesystem))
	if opts.EnablePatch {
		items = append(items, NewApplyPatchTool(filesystem))
	}
	if opts.EnableCommands {
		lints, err := NewReadLintsTool(filesystem, filesystem)
		if err != nil {
			return nil, err
		}
		items = append(items, lints)
		items = append(items, NewCommandTools(filesystem, opts.CommandTimeout)...)
	}
	return items, nil
}
