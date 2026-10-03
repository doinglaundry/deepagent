package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	filesystempkg "eino-cli/deepagent/graph/filesystem"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// FilesystemPrompt describes the shared local and Docker tool contracts.
const FilesystemPrompt = "Use filesystem tools to inspect and edit files. Paths are scoped to the configured filesystem. Read file offsets are one-based line numbers."

type FilesystemToolOptions struct {
	ReadOnly       bool
	EnableCommands bool
	EnablePatch    bool
	CommandTimeout time.Duration
}

// NewFilesystemTools is the only filesystem tool factory for local and Docker.
func NewFilesystemTools(filesystem filesystempkg.ToolFilesystem, opts FilesystemToolOptions) ([]ToolDescriptor, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("filesystem is required")
	}
	items := []ToolDescriptor{
		NewListFilesTool(filesystem), NewReadFileTool(filesystem),
		newFileSearchTool(filesystem, "glob"), newFileSearchTool(filesystem, "grep"),
		newFileSearchTool(filesystem, "rg"),
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

type ListFilesTool struct {
	backend filesystempkg.Filesystem
}

func NewListFilesTool(backend filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &ListFilesTool{backend: backend}, ReadOnly: true, ParallelSafe: true}
}

func (*ListFilesTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("list_files", "List files in a directory.", map[string]*schema.ParameterInfo{"path": {Type: schema.String}})
}

func (t *ListFilesTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	items, err := t.backend.List(ctx, in.Path)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(items), nil
}

func NewReadFileTool(backend interface {
	Read(context.Context, string, *int, *int) (string, error)
}) ToolDescriptor {
	return ToolDescriptor{Tool: &readFileTool{backend: backend}, ReadOnly: true, ParallelSafe: true}
}

type readFileTool struct {
	backend interface {
		Read(context.Context, string, *int, *int) (string, error)
	}
}

func (*readFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("read_file", "Read a UTF-8 file. Offset is a one-based line number.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "offset": {Type: schema.Integer}, "limit": {Type: schema.Integer}})
}

func (t *readFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Offset *int   `json:"offset"`
		Limit  *int   `json:"limit"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	if in.Offset != nil && *in.Offset > 0 {
		*in.Offset--
	}
	return t.backend.Read(ctx, in.Path, in.Offset, in.Limit)
}

type WriteFileTool struct{ backend filesystempkg.Filesystem }

func NewWriteFileTool(backend filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &WriteFileTool{backend: backend}, RequiresApproval: true}
}

func (*WriteFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("write_file", "Write a UTF-8 file.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "content": {Type: schema.String, Required: true}})
}

func (t *WriteFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", errors.New("path is required")
	}
	if in.Content == nil {
		return "", errors.New("content is required")
	}
	result, err := t.backend.Write(ctx, in.Path, *in.Content)
	if err != nil {
		return "", err
	}
	if result != nil && result.Error != "" {
		return "", result.Error
	}
	return "wrote " + in.Path, nil
}

type EditFileTool struct{ backend filesystempkg.Filesystem }

func NewEditFileTool(backend filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &EditFileTool{backend: backend}, RequiresApproval: true}
}

func (*EditFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("edit_file", "Replace an exact text span in a file; set replace_all to replace every occurrence.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "old": {Type: schema.String, Required: true}, "new": {Type: schema.String, Required: true}, "replace_all": {Type: schema.Boolean}})
}

func (t *EditFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path       string  `json:"path"`
		Old        string  `json:"old"`
		New        *string `json:"new"`
		ReplaceAll bool    `json:"replace_all"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", errors.New("path is required")
	}
	if in.New == nil {
		return "", errors.New("new is required")
	}
	result, err := t.backend.Edit(ctx, in.Path, in.Old, *in.New, in.ReplaceAll)
	if err != nil {
		return "", err
	}
	if result != nil && result.Error != "" {
		return "", result.Error
	}
	return "edited " + in.Path, nil
}

type DeleteFileTool struct{ workspace filesystempkg.Filesystem }

func NewDeleteFileTool(workspace filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &DeleteFileTool{workspace: workspace}, RequiresApproval: true}
}

func (*DeleteFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("delete_file", "Delete a file in the workspace.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}})
}

func (t *DeleteFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	return t.workspace.Delete(ctx, in.Path)
}

func NewApplyPatchTool(patcher filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &applyPatchTool{backend: patcher}, RequiresApproval: true}
}

type applyPatchTool struct{ backend filesystempkg.Filesystem }

func (*applyPatchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "apply_patch",
		Desc: "Apply a file-oriented patch inside the workspace.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"patch": {Type: schema.String, Required: true},
		}),
	}, nil
}

func (t *applyPatchTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var input struct {
		Patch string `json:"patch"`
	}
	err := json.Unmarshal([]byte(arguments), &input)
	if err != nil {
		return "", err
	}
	if input.Patch == "" {
		return "", fmt.Errorf("patch is required")
	}
	return t.backend.ApplyPatch(ctx, input.Patch)
}

func toolInfo(name, desc string, params map[string]*schema.ParameterInfo) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: name, Desc: desc, ParamsOneOf: schema.NewParamsOneOfByParams(params)}, nil
}
