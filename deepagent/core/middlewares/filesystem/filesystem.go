package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"eino-cli/deepagent/core/backends"
	"eino-cli/deepagent/core/middlewares"
	"eino-cli/deepagent/core/middlewares/execute"
	"eino-cli/deepagent/core/tools"
	einofs "github.com/cloudwego/eino/adk/filesystem"
	einofsmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type FilesystemConfig struct {
	Backend                                                            backends.Backend
	ReadOnly, DisableUploadDownload, DisableExecute, DisableApplyPatch bool
	WorkDir                                                            string
	CommandTimeout                                                     time.Duration
	ToolMask                                                           tools.Mask
}

type filesystemMiddleware struct {
	middleware.BaseMiddleware
	cfg *FilesystemConfig
}

func New(cfg *FilesystemConfig) middleware.Middleware { return &filesystemMiddleware{cfg: cfg} }
func (m *filesystemMiddleware) Name() string          { return "filesystem" }

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
	cfg := &einofsmw.Config{Backend: backendAdapter{m.cfg.Backend}}
	if m.cfg.ReadOnly {
		cfg.WriteFileToolConfig = &einofsmw.ToolConfig{Disable: true}
		cfg.EditFileToolConfig = &einofsmw.ToolConfig{Disable: true}
	}
	if !m.cfg.ReadOnly && !m.cfg.DisableExecute {
		if executor, ok := m.cfg.Backend.(backends.CommandExecutor); ok {
			cfg.Shell = shellAdapter{executor: executor, classifier: execute.NewDefaultClassifier(), timeout: m.cfg.CommandTimeout, workDir: m.cfg.WorkDir}
		}
	}
	built, err := einofsmw.NewMiddleware(ctx, cfg)
	if err != nil {
		return result, err
	}
	result.AdditionalInstruction = built.AdditionalInstruction
	result.AdditionalTools = built.AdditionalTools
	if workspace, ok := m.cfg.Backend.(backends.WorkspaceBackend); ok {
		result.AdditionalTools = append(result.AdditionalTools, newWorkspaceReadTools(workspace)...)
		if !m.cfg.ReadOnly {
			result.AdditionalTools = append(result.AdditionalTools, newDeleteFileTool(workspace))
			if !m.cfg.DisableExecute {
				result.AdditionalTools = append(result.AdditionalTools, newBackgroundShellTools(workspace, execute.NewDefaultClassifier())...)
			}
		}
	}
	if !m.cfg.ReadOnly && !m.cfg.DisableApplyPatch {
		if patcher, ok := m.cfg.Backend.(backends.ApplyPatchBackend); ok && patcher.SupportsApplyPatch() {
			result.AdditionalTools = append(result.AdditionalTools, &applyPatchTool{backend: patcher})
		}
	}
	return result, nil
}

type applyPatchTool struct{ backend backends.ApplyPatchBackend }

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
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "", err
	}
	if input.Patch == "" {
		return "", fmt.Errorf("patch is required")
	}
	return t.backend.ApplyPatch(ctx, input.Patch)
}

type backendAdapter struct{ backends.Backend }

func (b backendAdapter) LsInfo(ctx context.Context, req *einofs.LsInfoRequest) ([]einofs.FileInfo, error) {
	items, err := b.Backend.LsInfo(ctx, req.Path)
	result := make([]einofs.FileInfo, len(items))
	for i, item := range items {
		result[i] = einofs.FileInfo{Path: item.Path, IsDir: item.IsDir, Size: item.Size, ModifiedAt: item.ModifiedAt.Format(time.RFC3339)}
	}
	return result, err
}

func (b backendAdapter) Read(ctx context.Context, req *einofs.ReadRequest) (*einofs.FileContent, error) {
	var offset, limit *int
	if req.Offset > 0 {
		value := req.Offset - 1
		offset = &value
	}
	if req.Limit > 0 {
		limit = &req.Limit
	}
	content, err := b.Backend.Read(ctx, req.FilePath, offset, limit)
	return &einofs.FileContent{Content: content}, err
}

func (b backendAdapter) GrepRaw(ctx context.Context, req *einofs.GrepRequest) ([]einofs.GrepMatch, error) {
	items, err := b.Backend.GrepRaw(ctx, req.Pattern, req.Path, req.Glob)
	result := make([]einofs.GrepMatch, len(items))
	for i, item := range items {
		result[i] = einofs.GrepMatch{Path: item.Path, Line: item.Line, Content: item.Text}
	}
	return result, err
}

func (b backendAdapter) GlobInfo(ctx context.Context, req *einofs.GlobInfoRequest) ([]einofs.FileInfo, error) {
	items, err := b.Backend.GlobInfo(ctx, req.Pattern, req.Path)
	result := make([]einofs.FileInfo, len(items))
	for i, item := range items {
		result[i] = einofs.FileInfo{Path: item.Path, IsDir: item.IsDir, Size: item.Size, ModifiedAt: item.ModifiedAt.Format(time.RFC3339)}
	}
	return result, err
}

func (b backendAdapter) Write(ctx context.Context, req *einofs.WriteRequest) error {
	result, err := b.Backend.Write(ctx, req.FilePath, req.Content)
	if err == nil && result != nil && result.Error != "" {
		err = result.Error
	}
	return err
}

func (b backendAdapter) Edit(ctx context.Context, req *einofs.EditRequest) error {
	result, err := b.Backend.Edit(ctx, req.FilePath, req.OldString, req.NewString, req.ReplaceAll)
	if err == nil && result != nil && result.Error != "" {
		err = result.Error
	}
	return err
}

type shellAdapter struct {
	executor   backends.CommandExecutor
	classifier execute.CommandClassifier
	timeout    time.Duration
	workDir    string
}

func (s shellAdapter) Execute(ctx context.Context, req *einofs.ExecuteRequest) (*einofs.ExecuteResponse, error) {
	classification, err := s.classifier.Classify(ctx, execute.CommandSpec{
		Command: req.Command, RawCommand: req.Command, WorkDir: s.workDir, Timeout: s.timeout,
	})
	if err != nil {
		return nil, err
	}
	if classification.Classification != execute.ClassificationSafe {
		return nil, fmt.Errorf("command denied (%s): %s", classification.Classification, classification.Reason)
	}
	result, err := s.executor.ExecuteCommand(ctx, backends.CommandRequest{Command: req.Command, WorkDir: s.workDir, Timeout: s.timeout, MaxOutputBytes: backends.ToolResultTokenLimit * 4})
	if err != nil {
		return nil, err
	}
	exitCode := result.ExitCode
	return &einofs.ExecuteResponse{Output: result.Output, ExitCode: &exitCode, Truncated: result.Truncated}, nil
}

var _ einofs.Backend = backendAdapter{}
var _ einofs.Shell = shellAdapter{}
