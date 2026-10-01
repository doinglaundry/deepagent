//go:build !windows

package threadhost

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
	deepagents "eino-cli/deepagent/core"
	"eino-cli/deepagent/core/backend"
	longmemory "eino-cli/deepagent/core/memory"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	dalmodel "eino-cli/deepagent/dal/model"
	memorypkg "eino-cli/deepagent/protocol/memory"
	"eino-cli/deepagent/sandbox/aio"

	modelpkg "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// RuntimeConfig contains process-owned values used to build each Thread and
// each Run. Stable resources are resolved once when the Thread is created.
type RuntimeConfig struct {
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
	History          deepagents.HistoryRolloutStore
	Checkpoint       compose.CheckPointStore
	Tools            []tool.BaseTool
	SkillLoader      backend.SkillLoader
	MemoryStore      memorypkg.Store
	Collaboration    CollaborationBackend
	HistoryRecordID  deepagents.HistoryRecordIDProvider
	ApprovalRemember deepagents.ApprovalRememberer
	InterruptResume  deepagents.InterruptResumeDecoder
}

// createThread 准备资源和配置，再创建 Thread；初始化由 RunThread 负责。
func (w *ThreadHost) createThread(ctx context.Context, info *dalmodel.Thread) (thread *deepagents.Thread, err error) {
	// 1. 确定 Thread 身份、工作目录和模型。
	if info == nil || info.ThreadID == 0 {
		return nil, errors.New("threadhost: thread info is required")
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
	var filesystem backend.ToolFilesystem
	switch w.Runtime.FilesystemKind {
	case "", "local":
		filesystem, err = backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: workDir, VirtualMode: true}, threadID)
	case "docker":
		provider, releaseContainer, acquireErr := aio.AcquireDockerWorkspace(ctx, w.Runtime.Docker, info.SessionID+"-"+threadID, workDir)
		if acquireErr != nil {
			return nil, acquireErr
		}
		filesystem, err = backend.NewDockerFilesystem(provider, workDir, threadID, releaseContainer)
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
	agentConfig := deepagents.Config{
		Model: chatModel, MaxSteps: w.Runtime.MaxSteps, MaxModelCalls: w.Runtime.MaxModelCalls,
		CheckpointStore:  w.Deps.Checkpoint,
		ToolDescriptors:  []tools.ToolDescriptor{tools.Describe(tools.GetFollowUpTool())},
		SubAgents:        []*deepagents.SubAgent{{Name: "general-purpose", EnableFilesystem: true, EnableWeb: true}},
		SkillLoader:      w.Deps.SkillLoader,
		WebConfig:        w.Runtime.Web,
		Filesystem:       filesystem,
		FilesystemConfig: &deepagents.FilesystemConfig{},
	}
	for _, item := range w.Deps.Tools {
		agentConfig.ToolDescriptors = append(agentConfig.ToolDescriptors, tools.Describe(item))
	}
	prompt := strings.TrimSpace(w.Runtime.SystemPrompt)
	runConfig := &deepagents.RunConfig{Agent: agentConfig}
	runConfig.MiddlewaresProvider = func(context.Context, string) []middleware.Middleware {
		items := []middleware.Middleware{middleware.NewProjectInstructions(filesystem)}
		if w.Deps.Collaboration != nil {
			items = append(items, newCollaborationMiddleware(w.Deps.Collaboration, info))
		}
		if prompt != "" {
			items = append(items, middleware.NewBasePromptMiddleware(prompt))
		}
		if memoryService != nil {
			items = append(items, longmemory.NewPrompt(memoryService, memoryScope(w.Runtime.MemoryUserID, info)))
		}
		return items
	}
	if memoryService != nil {
		runConfig.RunCompleted = func(doneCtx context.Context, threadID, _ string, _ modelpkg.ToolCallingChatModel, history []*schema.Message) {
			observeErr := memoryService.Observe(doneCtx, memoryScope(w.Runtime.MemoryUserID, info), threadID, history)
			if observeErr != nil && !errors.Is(observeErr, memorypkg.ErrConflict) {
				slog.ErrorContext(doneCtx, "extract long-term memory", "thread_id", threadID, "error", observeErr)
				return
			}
			consolidateErr := memoryService.Consolidate(doneCtx, memoryScope(w.Runtime.MemoryUserID, info))
			if consolidateErr != nil && !errors.Is(consolidateErr, memorypkg.ErrConflict) {
				slog.ErrorContext(doneCtx, "consolidate long-term memory", "thread_id", threadID, "error", consolidateErr)
			}
		}
	}

	// 4. 配置 Thread 的历史和压缩，并绑定资源清理。
	options := deepagents.ThreadOptions{
		HistoryStore: w.Deps.History, ContextWindow: w.Runtime.ContextWindow,
		HistoryRecordID: w.Deps.HistoryRecordID,
	}
	if w.Runtime.CompactThresholdTokens > 0 {
		options.CompactionStrategy = &deepagents.SummaryCompaction{
			Model: chatModel, TokenLimit: w.Runtime.CompactThresholdTokens,
			KeepRecent: w.Runtime.KeepRecentMessages,
		}
	}
	threadConfig := deepagents.ThreadConfig{
		SessionID:        info.SessionID,
		ThreadID:         threadID,
		UserID:           info.UserID,
		RunConfig:        runConfig,
		Options:          options,
		ApprovalRemember: w.Deps.ApprovalRemember,
		InterruptResume:  w.Deps.InterruptResume,
		CloseResources:   filesystem.Close,
	}
	return deepagents.NewThread(threadConfig)
}

func (w *ThreadHost) memoryService(chatModel modelpkg.ToolCallingChatModel) (longmemory.Service, error) {
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

func memoryScope(configured string, info *dalmodel.Thread) string {
	value := strings.TrimSpace(configured)
	if value != "" {
		return "user/" + value
	}
	if info.UserID > 0 {
		return "user/" + strconv.FormatInt(info.UserID, 10)
	}
	return "session/" + info.SessionID
}
