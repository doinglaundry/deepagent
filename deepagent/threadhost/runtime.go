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
	"eino-cli/deepagent/core/backend"
	deepagents "eino-cli/deepagent/core/graph"
	longmemory "eino-cli/deepagent/core/memory"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/core/tools"
	dalmodel "eino-cli/deepagent/dal/model"
	inputpkg "eino-cli/deepagent/protocol/input"
	memorypkg "eino-cli/deepagent/protocol/memory"
	"eino-cli/deepagent/sandbox"
	"eino-cli/deepagent/sandbox/aio"
	threadpkg "eino-cli/deepagent/thread"
	modelpkg "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const defaultEventBusSize = 256

// RuntimeConfig contains process-owned values used to build each Thread and
// each Run. A RunConfig is intentionally rebuilt for every submitted run.
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
	History          agentthread.HistoryRolloutStore
	Checkpoint       compose.CheckPointStore
	Tools            []tool.BaseTool
	SkillLoader      backend.SkillLoader
	MemoryStore      memorypkg.Store
	Collaboration    CollaborationBackend
	HistoryRecordID  agentthread.HistoryRecordIDProvider
	ApprovalRemember threadpkg.ApprovalRememberer
	InterruptResume  threadpkg.InterruptResumeDecoder
}

func (w *ThreadHost) createDeepAgentThread(ctx context.Context, info *dalmodel.Thread) (threadpkg.ThreadRuntime, error) {
	if info == nil || info.ThreadID == 0 {
		return nil, errors.New("threadhost: thread info is required")
	}
	threadID := strconv.FormatInt(info.ThreadID, 10)
	roleID, workDir := "", ""
	if info.Profile != nil {
		roleID = strings.TrimSpace(info.Profile.Role)
		workDir = strings.TrimSpace(info.Profile.Cwd)
	}
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve workdir: %w", err)
		}
	}
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir: %w", err)
	}
	modelName := w.modelName(roleID)
	chatModel := w.Runtime.Models[modelName]
	if chatModel == nil {
		return nil, fmt.Errorf("model %q is unavailable", modelName)
	}
	var filesystem backend.ToolFilesystem
	cleanup := func() {}
	switch w.Runtime.FilesystemKind {
	case "", "local":
		filesystem, err = backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: workDir, VirtualMode: true}, threadID)
	case "docker":
		var provider sandbox.Sandbox
		provider, cleanup, err = aio.AcquireDockerWorkspace(ctx, w.Runtime.Docker, info.SessionID+"-"+threadID, workDir)
		if err == nil {
			filesystem, err = backend.NewDockerFilesystem(provider, workDir, threadID)
		}
	default:
		return nil, fmt.Errorf("unsupported filesystem kind %q", w.Runtime.FilesystemKind)
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	options := agentthread.ThreadOptions{
		HistoryStore: w.Deps.History, ContextWindow: w.Runtime.ContextWindow,
		HistoryRecordID: w.Deps.HistoryRecordID,
	}
	if w.Runtime.CompactThresholdTokens > 0 {
		options.CompactionStrategy = &agentthread.SummaryCompaction{
			Model: chatModel, TokenLimit: w.Runtime.CompactThresholdTokens,
			KeepRecent: w.Runtime.KeepRecentMessages,
		}
	}
	eventBuffer := w.Runtime.EventBuffer
	if eventBuffer <= 0 {
		eventBuffer = defaultEventBusSize
	}
	events := make(chan agentthread.Event, eventBuffer)
	deepThread := agentthread.New(threadID, nil, events, options)
	thread, err := threadpkg.NewThread(threadpkg.AdapterConfig{
		SessionID: info.SessionID,
		ThreadID:  threadID,
		ThreadInfo: threadpkg.ContextThreadIdentity{
			ThreadID: threadID, SessionID: info.SessionID, UserID: info.UserID,
		},
		Thread: deepThread, EventBus: events,
		RunConfig: func(runCtx context.Context, request threadpkg.RunStartRequest) (*agentthread.RunConfig, error) {
			return w.buildRunConfig(runCtx, info, roleID, filesystem, request.Mode)
		},
		ApprovalRemember: w.Deps.ApprovalRemember,
		InterruptResume:  w.Deps.InterruptResume,
	})
	if err != nil {
		_ = filesystem.Close(context.WithoutCancel(ctx))
		cleanup()
		return nil, err
	}
	return &filesystemThread{ThreadRuntime: thread, filesystem: filesystem, cleanup: cleanup}, nil
}

type filesystemThread struct {
	threadpkg.ThreadRuntime
	filesystem backend.ToolFilesystem
	cleanup    func()
}

func (t *filesystemThread) Close(ctx context.Context) error {
	err := errors.Join(t.ThreadRuntime.Close(ctx), t.filesystem.Close(context.WithoutCancel(ctx)))
	t.cleanup()
	return err
}

func (w *ThreadHost) buildRunConfig(
	ctx context.Context,
	info *dalmodel.Thread,
	roleID string,
	filesystem backend.ToolFilesystem,
	mode inputpkg.UserMessageMode,
) (*agentthread.RunConfig, error) {
	modelName := w.modelName(roleID)
	chatModel := w.Runtime.Models[modelName]
	if chatModel == nil {
		return nil, fmt.Errorf("model %q is unavailable", modelName)
	}
	agentConfig := deepagents.Config{
		Model: chatModel, MaxSteps: w.Runtime.MaxSteps, MaxModelCalls: w.Runtime.MaxModelCalls,
		CheckpointStore: w.Deps.Checkpoint, EnablePatchToolCalls: true,
		HITLConfig:       &deepagents.HITLConfig{NeedFollowUpTool: true},
		SkillLoader:      w.Deps.SkillLoader,
		WebConfig:        w.Runtime.Web,
		Filesystem:       filesystem,
		FilesystemConfig: &deepagents.FilesystemConfig{},
	}
	for _, item := range w.Deps.Tools {
		agentConfig.ToolDescriptors = append(agentConfig.ToolDescriptors, tools.Describe(item))
	}
	agentConfig.Middlewares = append(agentConfig.Middlewares, middleware.NewProjectInstructions(filesystem))
	if w.Deps.Collaboration != nil {
		agentConfig.Middlewares = append(agentConfig.Middlewares, newCollaborationMiddleware(w.Deps.Collaboration, info))
	}
	prompt := strings.TrimSpace(w.Runtime.SystemPrompt)
	memoryService, err := w.memoryService(chatModel)
	if err != nil {
		return nil, err
	}

	if prompt != "" {
		agentConfig.Middlewares = append(agentConfig.Middlewares, middleware.NewBasePromptMiddleware(prompt))
	}
	if memoryService != nil {
		agentConfig.Middlewares = append(agentConfig.Middlewares, longmemory.NewPrompt(memoryService, memoryScope(w.Runtime.MemoryUserID, info)))
	}
	runConfig := &agentthread.RunConfig{
		Agent: agentConfig, EnablePlan: mode == inputpkg.UserMessageModeImplPlan,
	}
	if memoryService != nil {
		runConfig.RunCompleted = func(doneCtx context.Context, threadID, _ string, _ modelpkg.ToolCallingChatModel, history []*schema.Message) {
			if observeErr := memoryService.Observe(doneCtx, memoryScope(w.Runtime.MemoryUserID, info), threadID, history); observeErr != nil && !errors.Is(observeErr, memorypkg.ErrConflict) {
				slog.ErrorContext(doneCtx, "extract long-term memory", "thread_id", threadID, "error", observeErr)
				return
			}
			if consolidateErr := memoryService.Consolidate(doneCtx, memoryScope(w.Runtime.MemoryUserID, info)); consolidateErr != nil && !errors.Is(consolidateErr, memorypkg.ErrConflict) {
				slog.ErrorContext(doneCtx, "consolidate long-term memory", "thread_id", threadID, "error", consolidateErr)
			}
		}
	}
	return runConfig, nil
}

func (w *ThreadHost) modelName(roleID string) string {
	if name := strings.TrimSpace(w.Runtime.RoleModels[roleID]); name != "" {
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
	if value := strings.TrimSpace(configured); value != "" {
		return "user/" + value
	}
	if info.UserID > 0 {
		return "user/" + strconv.FormatInt(info.UserID, 10)
	}
	return "session/" + info.SessionID
}
