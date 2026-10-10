//go:build !windows

package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"eino-cli/deepagent/config"
	"eino-cli/deepagent/graph/computer"
	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/execution"
	filesystempkg "eino-cli/deepagent/graph/filesystem"
	longmemory "eino-cli/deepagent/graph/memory"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"
	"eino-cli/deepagent/run"
	"eino-cli/deepagent/sandbox/aio"
	threadpkg "eino-cli/deepagent/thread"

	modelpkg "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// RuntimeConfig contains process-owned values used to build each Thread and
// each Run. Stable resources are resolved once when the Thread is created.
type RuntimeConfig struct {
	BrowserOrigins         []string
	ComputerApps           []string
	FilesystemKind         string
	Docker                 config.SandboxConfig
	Models                 map[string]modelpkg.ToolCallingChatModel
	DefaultModel           string
	SystemPrompt           string
	MaxSteps               int
	MaxModelCalls          int
	ContextWindow          int64
	CompactThresholdTokens int64
	KeepRecentMessages     int
	Web                    *tools.WebConfig
	MemoryEnabled          bool
	MemoryDir              string
	MemoryUserID           string
	MemoryLeaseTTL         time.Duration
}

// RuntimeDeps are long-lived resources shared by Thread runtimes.
type RuntimeDeps struct {
	Desktop             *computer.Desktop
	ConversationDB      agentmodel.ConversationDB
	Checkpoint          compose.CheckPointStore
	Tools               []agentmodel.ToolDescriptor
	SkillLoader         agentmodel.SkillLoader
	MemoryStore         agentmodel.MemoryStore
	Collaboration       agentmodel.CollaborationBackend
	GenerateMessageID   agentmodel.GetMessageIDFunc
	ApprovalRemember    agentmodel.ApprovalRememberer
	IsToolAlwaysAllowed func(context.Context, int64, string) (bool, error)
	InterruptResume     agentmodel.InterruptResumeDecoder
}

// createThread 准备资源和配置，再创建 Thread；初始化由 RunThread 负责。
func (w *Worker) createThread(ctx context.Context, info *agentmodel.ThreadRecord) (thread *threadpkg.Thread, err error) {
	// 1. 确定 Thread 身份、工作目录和模型。
	if info == nil || info.ThreadID == 0 {
		return nil, errors.New("worker: thread info is required")
	}
	threadID := strconv.FormatInt(info.ThreadID, 10)
	workDir := ""
	if info.Profile != nil {
		workDir = strings.TrimSpace(info.Profile.Cwd)
	}
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir: %w", err)
	}
	chatModel := w.Runtime.Models[w.Runtime.DefaultModel]
	if chatModel == nil {
		return nil, fmt.Errorf("model %q is unavailable", w.Runtime.DefaultModel)
	}
	// 配置错误在分配文件系统之前返回。
	memoryService, err := w.memoryService(chatModel)
	if err != nil {
		return nil, err
	}

	// 2. 创建整个 Thread 共用的文件系统。
	var filesystem agentmodel.ToolFilesystem
	switch w.Runtime.FilesystemKind {
	case "", "local":
		filesystem, err = filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: workDir, VirtualMode: true}, threadID)
	case "docker":
		provider, releaseContainer, acquireErr := aio.AcquireDockerWorkspace(ctx, w.Runtime.Docker, info.SessionID+"-"+threadID, workDir)
		if acquireErr != nil {
			return nil, acquireErr
		}
		filesystem, err = filesystempkg.NewDockerFilesystem(provider, workDir, threadID, releaseContainer)
	default:
		return nil, fmt.Errorf("unsupported filesystem kind %q", w.Runtime.FilesystemKind)
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		if thread == nil {
			err = errors.Join(err, filesystem.Close(context.WithoutCancel(ctx)))
		}
	}()

	// 3. 配置 Run 的模型、工具、checkpoint 和每次执行独立的 middleware。
	agentConfig := execution.Config{
		Model: chatModel, MaxSteps: w.Runtime.MaxSteps, MaxModelCalls: w.Runtime.MaxModelCalls,
		CheckpointStore:  w.Deps.Checkpoint,
		ToolDescriptors:  []agentmodel.ToolDescriptor{tools.NewFollowUpTool()},
		SubAgents:        []*execution.SubAgent{{Name: "general-purpose", EnableFilesystem: true, EnableWeb: true}},
		SkillLoader:      w.Deps.SkillLoader,
		WebConfig:        w.Runtime.Web,
		Filesystem:       filesystem,
		FilesystemConfig: &execution.FilesystemConfig{},
	}
	if w.LocalModel != nil {
		agentConfig.LocalModelParametersFingerprint = w.LocalModel.GetParametersFingerprint()
	}
	agentConfig.Policy = agentmodel.PolicyFunc(func(ctx context.Context, call agentmodel.ToolCall, descriptor agentmodel.ToolDescriptor) (agentmodel.Decision, error) {
		scopeErr := validateComputerTarget(w.Runtime, call)
		if scopeErr != nil {
			return agentmodel.Decision{Action: agentmodel.Deny, Reason: scopeErr.Error()}, nil
		}
		if !descriptor.RequiresApproval {
			return agentmodel.Decision{Action: agentmodel.Allow}, nil
		}
		if w.Deps.IsToolAlwaysAllowed == nil {
			return agentmodel.Decision{Action: agentmodel.AskApproval}, nil
		}
		allowed, err := w.Deps.IsToolAlwaysAllowed(ctx, info.ThreadID, call.Name)
		if err != nil {
			return agentmodel.Decision{}, err
		}
		if allowed {
			return agentmodel.Decision{Action: agentmodel.Allow}, nil
		}
		return agentmodel.Decision{Action: agentmodel.AskApproval}, nil
	})
	agentConfig.ToolDescriptors = append(agentConfig.ToolDescriptors, w.Deps.Tools...)
	if w.Deps.Collaboration != nil {
		items, err := newCollaborationTools(w.Deps.Collaboration, info)
		if err != nil {
			return nil, err
		}
		agentConfig.ToolDescriptors = append(agentConfig.ToolDescriptors, items...)
		agentConfig.Prompts = append(agentConfig.Prompts, agentmodel.NewSystemMessage(collaborationPrompt))
	}
	prompt := strings.TrimSpace(w.Runtime.SystemPrompt)
	if prompt != "" {
		agentConfig.Prompts = append(agentConfig.Prompts, agentmodel.NewSystemMessage(prompt))
	}
	agentConfig.Middlewares = []agentmodel.Middleware{middleware.NewProjectInstructions(filesystem)}
	if w.Deps.Desktop != nil {
		browser, err := w.getThreadBrowser(ctx, info.ThreadID)
		if err != nil {
			return nil, err
		}
		agentConfig.ToolDescriptors = append(agentConfig.ToolDescriptors, tools.NewBrowserTools(browser)...)
		agentConfig.ToolDescriptors = append(agentConfig.ToolDescriptors, tools.NewComputerTools(w.Deps.Desktop)...)
		agentConfig.Middlewares = append(agentConfig.Middlewares, middleware.NewComputer(w.Deps.Desktop))
		agentConfig.Prompts = append(agentConfig.Prompts, agentmodel.NewSystemMessage(tools.ComputerPrompt))
		agentConfig.SubAgents[0].ToolMask = func(_ context.Context, info *schema.ToolInfo) bool {
			return !strings.HasPrefix(info.Name, "browser_") && !strings.HasPrefix(info.Name, "computer_")
		}
	}
	if memoryService != nil {
		agentConfig.Middlewares = append(agentConfig.Middlewares, longmemory.NewPrompt(memoryService, memoryScope(w.Runtime.MemoryUserID, info)))
	}
	runConfig := &run.Config{Graph: agentConfig}
	if memoryService != nil {
		runConfig.RunCompleted = func(doneCtx context.Context, threadID, _ string, _ modelpkg.ToolCallingChatModel, history []*agentmodel.Message) {
			observeErr := memoryService.Observe(doneCtx, memoryScope(w.Runtime.MemoryUserID, info), threadID, history)
			if observeErr != nil && !errors.Is(observeErr, agentmodel.ErrMemoryConflict) {
				slog.ErrorContext(doneCtx, "extract long-term memory", "thread_id", threadID, "error", observeErr)
				return
			}
			consolidateErr := memoryService.Consolidate(doneCtx, memoryScope(w.Runtime.MemoryUserID, info))
			if consolidateErr != nil && !errors.Is(consolidateErr, agentmodel.ErrMemoryConflict) {
				slog.ErrorContext(doneCtx, "consolidate long-term memory", "thread_id", threadID, "error", consolidateErr)
			}
		}
	}

	// 4. 配置 Thread 的历史和压缩，并绑定资源清理。
	options := threadpkg.ThreadOptions{
		ConversationDB: w.Deps.ConversationDB, ContextWindow: w.Runtime.ContextWindow,
		GenerateMessageID: w.Deps.GenerateMessageID,
	}
	if w.Runtime.CompactThresholdTokens > 0 {
		options.Compactor = &conversation.SummaryCompaction{
			Model: chatModel, TokenLimit: w.Runtime.CompactThresholdTokens,
			KeepRecent: w.Runtime.KeepRecentMessages,
		}
	}
	threadConfig := threadpkg.ThreadConfig{
		SessionID:        info.SessionID,
		ThreadID:         threadID,
		UserID:           info.UserID,
		RunConfig:        runConfig,
		Options:          options,
		ApprovalRemember: w.Deps.ApprovalRemember,
		InterruptResume:  w.Deps.InterruptResume,
		CloseResources:   filesystem.Close,
	}
	return threadpkg.NewThread(threadConfig)
}

func (w *Worker) memoryService(chatModel modelpkg.ToolCallingChatModel) (agentmodel.MemoryService, error) {
	if !w.Runtime.MemoryEnabled {
		return nil, nil
	}
	service, err := longmemory.New(longmemory.Config{
		Store: w.Deps.MemoryStore, LeaseTTL: w.Runtime.MemoryLeaseTTL,
		Root: w.Runtime.MemoryDir, Model: chatModel,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize memory: %w", err)
	}
	return service, nil
}

func memoryScope(configured string, info *agentmodel.ThreadRecord) string {
	value := strings.TrimSpace(configured)
	if value != "" {
		return "user/" + value
	}
	if info.UserID > 0 {
		return "user/" + strconv.FormatInt(info.UserID, 10)
	}
	return "session/" + info.SessionID
}
