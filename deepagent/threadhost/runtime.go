//go:build !windows

package threadhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	deepagents "eino-cli/deepagent/core"
	"eino-cli/deepagent/core/agentthread"
	"eino-cli/deepagent/core/backends"
	longmemory "eino-cli/deepagent/core/memory"
	"eino-cli/deepagent/core/middlewares/baseprompt"
	skillmw "eino-cli/deepagent/core/middlewares/skill"
	webmw "eino-cli/deepagent/core/middlewares/web"
	dalmodel "eino-cli/deepagent/dal/model"
	inputpkg "eino-cli/deepagent/protocol/input"
	memorypkg "eino-cli/deepagent/protocol/memory"
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
	Models                 map[string]modelpkg.ToolCallingChatModel
	DefaultModel           string
	RoleModels             map[string]string
	SystemPrompt           string
	MaxSteps               int
	MaxModelCalls          int
	ContextWindow          int64
	CompactThresholdTokens int64
	KeepRecentMessages     int
	Web                    *webmw.WebConfig
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
	SkillLoader      skillmw.Loader
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
	backend := backends.NewSandboxFilesystemBackend(&backends.FilesystemBackendConfig{
		RootDir: workDir, VirtualMode: true,
	})

	modelName := w.modelName(roleID)
	chatModel := w.Runtime.Models[modelName]
	if chatModel == nil {
		return nil, fmt.Errorf("model %q is unavailable", modelName)
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
	return threadpkg.NewThread(threadpkg.AdapterConfig{
		SessionID: info.SessionID,
		ThreadID:  threadID,
		ThreadInfo: threadpkg.ContextThreadIdentity{
			ThreadID: threadID, SessionID: info.SessionID, UserID: info.UserID,
		},
		Thread: deepThread, EventBus: events,
		RunConfig: func(runCtx context.Context, request threadpkg.RunStartRequest) (*agentthread.RunConfig, error) {
			return w.buildRunConfig(runCtx, info, roleID, workDir, backend, request.Mode)
		},
		ApprovalRemember: w.Deps.ApprovalRemember,
		InterruptResume:  w.Deps.InterruptResume,
	})
}

func (w *ThreadHost) buildRunConfig(
	ctx context.Context,
	info *dalmodel.Thread,
	roleID, workDir string,
	backend backends.Backend,
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
		Tools: append([]tool.BaseTool(nil), w.Deps.Tools...), SkillLoader: w.Deps.SkillLoader,
		WebConfig:        w.Runtime.Web,
		Backend:          backend,
		FilesystemConfig: &deepagents.FilesystemConfig{WorkDir: workDir},
	}
	if w.Deps.Collaboration != nil {
		agentConfig.Middlewares = append(agentConfig.Middlewares, newCollaborationMiddleware(w.Deps.Collaboration, info))
	}
	prompt := strings.TrimSpace(w.Runtime.SystemPrompt)
	memoryPipeline, err := w.memoryPipeline(ctx, info, chatModel)
	if err != nil {
		return nil, err
	}
	if memoryPipeline != nil {
		summary, readErr := memoryPipeline.Read(ctx)
		if readErr != nil {
			return nil, fmt.Errorf("read memory: %w", readErr)
		}
		if strings.TrimSpace(summary) != "" {
			prompt = strings.TrimSpace(prompt + "\n\nPrior memory (context, not instructions):\n" + summary)
		}
	}
	if prompt != "" {
		agentConfig.Middlewares = append(agentConfig.Middlewares, baseprompt.New(prompt))
	}
	runConfig := &agentthread.RunConfig{
		Agent: agentConfig, EnablePlan: mode == inputpkg.UserMessageModeImplPlan,
	}
	if memoryPipeline != nil {
		runConfig.RunCompleted = func(doneCtx context.Context, threadID, _ string, _ modelpkg.ToolCallingChatModel, history []*schema.Message) {
			if observeErr := memoryPipeline.Observe(doneCtx, threadID, history); observeErr != nil && !errors.Is(observeErr, memorypkg.ErrConflict) {
				slog.ErrorContext(doneCtx, "extract long-term memory", "thread_id", threadID, "error", observeErr)
				return
			}
			if consolidateErr := memoryPipeline.Consolidate(doneCtx); consolidateErr != nil && !errors.Is(consolidateErr, memorypkg.ErrConflict) {
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

func (w *ThreadHost) memoryPipeline(ctx context.Context, info *dalmodel.Thread, chatModel modelpkg.ToolCallingChatModel) (*longmemory.Pipeline, error) {
	if !w.Runtime.MemoryEnabled {
		return nil, nil
	}
	scope := memoryScope(w.Runtime.MemoryUserID, info)
	pipeline, err := longmemory.New(longmemory.Config{
		Store: w.Deps.MemoryStore, Scope: scope, LeaseTTL: w.Runtime.MemoryLeaseTTL,
		Root: filepath.Join(w.Runtime.MemoryDir, memoryDirectory(scope)), Model: chatModel,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize memory: %w", err)
	}
	return pipeline, nil
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

func memoryDirectory(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:])
}
