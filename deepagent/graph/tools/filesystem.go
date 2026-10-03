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
func NewFilesystemTools(filesystem filesystempkg.ToolFilesystem, options FilesystemToolOptions) ([]ToolDescriptor, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("filesystem is required")
	}
	toolDescriptors := []ToolDescriptor{
		NewListFilesTool(filesystem), NewReadFileTool(filesystem),
		newFileSearchTool(filesystem, "glob"), newFileSearchTool(filesystem, "grep"),
		newFileSearchTool(filesystem, "rg"),
	}
	semanticSearchDescriptor, err := NewSemanticSearchTool(filesystem)
	if err != nil {
		return nil, err
	}
	toolDescriptors = append(toolDescriptors, semanticSearchDescriptor)
	if options.ReadOnly {
		return toolDescriptors, nil
	}
	toolDescriptors = append(toolDescriptors, NewWriteFileTool(filesystem), NewEditFileTool(filesystem), NewDeleteFileTool(filesystem))
	if options.EnablePatch {
		toolDescriptors = append(toolDescriptors, NewApplyPatchTool(filesystem))
	}
	if options.EnableCommands {
		lintDescriptor, err := NewReadLintsTool(filesystem, filesystem)
		if err != nil {
			return nil, err
		}
		toolDescriptors = append(toolDescriptors, lintDescriptor)
		toolDescriptors = append(toolDescriptors, NewCommandTools(filesystem, options.CommandTimeout)...)
	}
	return toolDescriptors, nil
}

type ListFilesTool struct {
	filesystem filesystempkg.Filesystem
}

func NewListFilesTool(filesystem filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &ListFilesTool{filesystem: filesystem}, ReadOnly: true, ParallelSafe: true}
}

func (*ListFilesTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newToolInfo("list_files", "List files in a directory.", map[string]*schema.ParameterInfo{"path": {Type: schema.String}})
}

func (listFilesTool *ListFilesTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var listArgs struct {
		Path string `json:"path"`
	}
	err := json.Unmarshal([]byte(arguments), &listArgs)
	if err != nil {
		return "", err
	}
	files, err := listFilesTool.filesystem.List(ctx, listArgs.Path)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(files), nil
}

func NewReadFileTool(fileReader interface {
	Read(context.Context, string, *int, *int) (string, error)
}) ToolDescriptor {
	return ToolDescriptor{Tool: &readFileTool{fileReader: fileReader}, ReadOnly: true, ParallelSafe: true}
}

type readFileTool struct {
	fileReader interface {
		Read(context.Context, string, *int, *int) (string, error)
	}
}

func (*readFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newToolInfo("read_file", "Read a UTF-8 file. Offset is a one-based line number.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "offset": {Type: schema.Integer}, "limit": {Type: schema.Integer}})
}

func (readFileTool *readFileTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var readArgs struct {
		Path   string `json:"path"`
		Offset *int   `json:"offset"`
		Limit  *int   `json:"limit"`
	}
	err := json.Unmarshal([]byte(arguments), &readArgs)
	if err != nil {
		return "", err
	}
	if readArgs.Offset != nil && *readArgs.Offset > 0 {
		*readArgs.Offset--
	}
	return readFileTool.fileReader.Read(ctx, readArgs.Path, readArgs.Offset, readArgs.Limit)
}

type WriteFileTool struct{ filesystem filesystempkg.Filesystem }

func NewWriteFileTool(filesystem filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &WriteFileTool{filesystem: filesystem}, RequiresApproval: true}
}

func (*WriteFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newToolInfo("write_file", "Write a UTF-8 file.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "content": {Type: schema.String, Required: true}})
}

func (writeFileTool *WriteFileTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var writeArgs struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	err := json.Unmarshal([]byte(arguments), &writeArgs)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(writeArgs.Path) == "" {
		return "", errors.New("path is required")
	}
	if writeArgs.Content == nil {
		return "", errors.New("content is required")
	}
	writeResult, err := writeFileTool.filesystem.Write(ctx, writeArgs.Path, *writeArgs.Content)
	if err != nil {
		return "", err
	}
	if writeResult != nil && writeResult.Error != "" {
		return "", writeResult.Error
	}
	return "wrote " + writeArgs.Path, nil
}

type EditFileTool struct{ filesystem filesystempkg.Filesystem }

func NewEditFileTool(filesystem filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &EditFileTool{filesystem: filesystem}, RequiresApproval: true}
}

func (*EditFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newToolInfo("edit_file", "Replace an exact text span in a file; set replace_all to replace every occurrence.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "old": {Type: schema.String, Required: true}, "new": {Type: schema.String, Required: true}, "replace_all": {Type: schema.Boolean}})
}

func (editFileTool *EditFileTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var editArgs struct {
		Path       string  `json:"path"`
		Old        string  `json:"old"`
		New        *string `json:"new"`
		ReplaceAll bool    `json:"replace_all"`
	}
	err := json.Unmarshal([]byte(arguments), &editArgs)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(editArgs.Path) == "" {
		return "", errors.New("path is required")
	}
	if editArgs.New == nil {
		return "", errors.New("new is required")
	}
	editResult, err := editFileTool.filesystem.Edit(ctx, editArgs.Path, editArgs.Old, *editArgs.New, editArgs.ReplaceAll)
	if err != nil {
		return "", err
	}
	if editResult != nil && editResult.Error != "" {
		return "", editResult.Error
	}
	return "edited " + editArgs.Path, nil
}

type DeleteFileTool struct{ filesystem filesystempkg.Filesystem }

func NewDeleteFileTool(filesystem filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &DeleteFileTool{filesystem: filesystem}, RequiresApproval: true}
}

func (*DeleteFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newToolInfo("delete_file", "Delete a file in the workspace.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}})
}

func (deleteFileTool *DeleteFileTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var deleteArgs struct {
		Path string `json:"path"`
	}
	err := json.Unmarshal([]byte(arguments), &deleteArgs)
	if err != nil {
		return "", err
	}
	return deleteFileTool.filesystem.Delete(ctx, deleteArgs.Path)
}

func NewApplyPatchTool(filesystem filesystempkg.Filesystem) ToolDescriptor {
	return ToolDescriptor{Tool: &applyPatchTool{filesystem: filesystem}, RequiresApproval: true}
}

type applyPatchTool struct{ filesystem filesystempkg.Filesystem }

func (*applyPatchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "apply_patch",
		Desc: "Apply a file-oriented patch inside the workspace.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"patch": {Type: schema.String, Required: true},
		}),
	}, nil
}

func (applyPatchTool *applyPatchTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var patchArgs struct {
		Patch string `json:"patch"`
	}
	err := json.Unmarshal([]byte(arguments), &patchArgs)
	if err != nil {
		return "", err
	}
	if patchArgs.Patch == "" {
		return "", fmt.Errorf("patch is required")
	}
	return applyPatchTool.filesystem.ApplyPatch(ctx, patchArgs.Patch)
}

func newToolInfo(toolName, description string, parameters map[string]*schema.ParameterInfo) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: toolName, Desc: description, ParamsOneOf: schema.NewParamsOneOfByParams(parameters)}, nil
}
