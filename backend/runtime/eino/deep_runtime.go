package eino

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent"
	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	"eino-cli/backend/session/checkpoint"
)

type DeepAgentRuntime struct {
	cfg                 *config.Config
	modelName           string
	runner              *adk.Runner
	mu                  sync.Mutex
	pendingCheckpointID string
	history             []*schema.Message
	maxHistoryTurns     int
	trace               *middlewares.Trace
	planMode            atomic.Bool
}

func NewDeepAgentRuntime(ctx context.Context, cfg *config.Config) (Runtime, error) {
	r := &DeepAgentRuntime{
		cfg:             cfg,
		modelName:       cfg.DefaultModel,
		maxHistoryTurns: 20,
	}
	runner, trace, err := buildLeadRunner(ctx, cfg, r.planMode.Load)
	if err != nil {
		return nil, err
	}
	r.runner = runner
	r.trace = trace
	return r, nil
}

func buildLeadRunner(ctx context.Context, cfg *config.Config, getPlanMode func() bool) (*adk.Runner, *middlewares.Trace, error) {
	leadAgent, trace, err := agent.MakeLeadAgent(ctx, "default", true, getPlanMode, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build lead agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
		CheckPointStore: checkpoint.NewStore(filepath.Join(cfg.RootDir, ".eino-cli", "checkpoints")),
	})
	return runner, trace, nil
}

func (r *DeepAgentRuntime) ExecuteStream(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Result{}, fmt.Errorf("prompt is required")
	}

	r.mu.Lock()
	if len(r.history) > r.maxHistoryTurns*2 {
		r.history = r.history[len(r.history)-r.maxHistoryTurns*2:]
	}

	messages := make([]*schema.Message, len(r.history)+1)
	copy(messages, r.history)
	messages[len(messages)-1] = schema.UserMessage(prompt)
	runner := r.runner
	r.mu.Unlock()

	// CLI / single-tenant default: stamp a fixed thread id so the sandbox
	// middleware enters its per-thread mapping branch instead of falling
	// back to the generic singleton. Server mode has already stamped the
	// real tid via gateway middleware; guard keeps that intact.
	if middlewares.GetThreadID(ctx) == "" {
		ctx = middlewares.WithThreadID(ctx, "cli")
	}

	checkpointID := fmt.Sprintf("ckpt-%d", time.Now().UnixNano())
	iter := runner.Run(ctx, messages, adk.WithCheckPointID(checkpointID))
	summary, err := collectAgentEventsWithSink(iter, onChunk)
	if err != nil {
		return Result{}, err
	}

	if summary.Interrupted {
		r.mu.Lock()
		r.pendingCheckpointID = checkpointID
		r.mu.Unlock()
		return Result{Success: false, Code: ErrorCodeRuntime, Message: "execution interrupted", NeedsUser: true}, nil
	}

	if strings.TrimSpace(summary.Output) == "" {
		return Result{}, fmt.Errorf("deep runtime returned empty output")
	}

	r.mu.Lock()
	r.history = append(r.history, schema.UserMessage(prompt), schema.AssistantMessage(summary.Output, nil))
	r.mu.Unlock()

	return SuccessResult(summary.Output), nil
}

func (r *DeepAgentRuntime) ClearHistory() {
	r.mu.Lock()
	r.history = nil
	r.mu.Unlock()
	if r.trace != nil {
		r.trace.ResetTurn()
	}
}

func (r *DeepAgentRuntime) SetPlanMode(_ context.Context, on bool) (bool, error) {
	r.planMode.Store(on)
	return on, nil
}

func (r *DeepAgentRuntime) Name() string {
	if strings.TrimSpace(r.modelName) == "" {
		return "deep-agent"
	}
	return strings.TrimSpace(r.modelName)
}
