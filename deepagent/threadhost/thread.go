//go:build !windows

package threadhost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	"eino-cli/deepagent/sandbox"
	"eino-cli/deepagent/sandbox/aio"

	modelpkg "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const defaultEventBusSize = 256

// RuntimeConfig contains process-owned values used to build each Thread and
// each Run. Stable resources are resolved once when the Thread is created.
type RuntimeConfig struct {
	FilesystemKind         string
	Docker                 config.SandboxConfig
	Models                 map[string]modelpkg.ToolCallingChatModel
	DefaultModel           string
	RoleModels             map[string]string
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
	EventBuffer            int
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

func (w *ThreadHost) createThread(ctx context.Context, info *dalmodel.Thread) (thread *deepagents.Thread, output *deepagents.TransportThreadOutput, err error) {
	if info == nil || info.ThreadID == 0 {
		return nil, nil, errors.New("threadhost: thread info is required")
	}
	threadID := strconv.FormatInt(info.ThreadID, 10)
	roleID, workDir := "", ""
	if info.Profile != nil {
		roleID = strings.TrimSpace(info.Profile.Role)
		workDir = strings.TrimSpace(info.Profile.Cwd)
	}
	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			return nil, nil, fmt.Errorf("resolve workdir: %w", err)
		}
	}
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve workdir: %w", err)
	}
	modelName := w.modelName(roleID)
	chatModel := w.Runtime.Models[modelName]
	if chatModel == nil {
		return nil, nil, fmt.Errorf("model %q is unavailable", modelName)
	}
	var filesystem backend.ToolFilesystem
	var cleanup func()
	switch w.Runtime.FilesystemKind {
	case "", "local":
		filesystem, err = backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: workDir, VirtualMode: true}, threadID)
	case "docker":
		var provider sandbox.Sandbox
		provider, cleanup, err = aio.AcquireDockerWorkspace(ctx, w.Runtime.Docker, info.SessionID+"-"+threadID, workDir)
		if err != nil {
			return nil, nil, err
		}
		filesystem, err = backend.NewDockerFilesystem(provider, workDir, threadID)
	default:
		return nil, nil, fmt.Errorf("unsupported filesystem kind %q", w.Runtime.FilesystemKind)
	}
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, nil, err
	}
	closeResources := func(closeCtx context.Context) error {
		closeErr := filesystem.Close(closeCtx)
		if closeErr != nil {
			return closeErr
		}
		if cleanup != nil {
			cleanup()
		}
		return nil
	}
	defer func() {
		// Once constructed, Thread owns cleanup, including an Init failure.
		if thread == nil {
			err = errors.Join(err, closeResources(context.WithoutCancel(ctx)))
		}
	}()
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
	eventBuffer := w.Runtime.EventBuffer
	if eventBuffer <= 0 {
		eventBuffer = defaultEventBusSize
	}
	events := make(chan deepagents.Event, eventBuffer)
	runConfig, err := w.buildRunConfig(info, chatModel, filesystem)
	if err != nil {
		return nil, nil, err
	}
	thread, err = deepagents.NewThread(deepagents.ThreadConfig{
		SessionID:  info.SessionID,
		ThreadID:   threadID,
		ThreadInfo: deepagents.ContextThreadIdentity{ThreadID: threadID, SessionID: info.SessionID, UserID: info.UserID},
		RunConfig:  runConfig, Events: events, Options: options,
		ApprovalRemember: w.Deps.ApprovalRemember,
		InterruptResume:  w.Deps.InterruptResume,
		CloseResources:   closeResources,
	})
	if err != nil {
		return nil, nil, err
	}
	output, err = thread.Init(ctx)
	if err != nil {
		return thread, nil, fmt.Errorf("Thread.Init thread_id=%d: %w", info.ThreadID, err)
	}
	return thread, output, nil
}

func (w *ThreadHost) buildRunConfig(
	info *dalmodel.Thread,
	chatModel modelpkg.ToolCallingChatModel,
	filesystem backend.ToolFilesystem,
) (*deepagents.RunConfig, error) {
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
	memoryService, err := w.memoryService(chatModel)
	if err != nil {
		return nil, err
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
	return runConfig, nil
}

func (w *ThreadHost) modelName(roleID string) string {
	name := strings.TrimSpace(w.Runtime.RoleModels[roleID])
	if name != "" {
		return name
	}
	return w.Runtime.DefaultModel
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
