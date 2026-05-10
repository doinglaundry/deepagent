package eino

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent"
	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	"eino-cli/backend/session/checkpoint"
)

type DeepAgentRuntime struct {
	modelName           string
	runner              *adk.Runner
	mu                  sync.Mutex
	pendingCheckpointID string
	history             []*schema.Message
	maxHistoryTurns     int
	// trace is the lead agent's debug-trace; nil-safe; used only by ClearHistory.
	trace *middlewares.Trace
}

// NewDeepAgentRuntime builds the runtime context, the lead agent, and the
// adk.Runner; history / checkpoint / streaming live here (REPL-owned).
func NewDeepAgentRuntime(ctx context.Context, cfg config.Config) (Runtime, error) {
	runtimeCtx, err := agent.NewRuntimeContext(cfg, nil)
	if err != nil {
		return nil, err
	}

	leadAgent, trace, err := agent.MakeLeadAgent(ctx, runtimeCtx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build lead agent: %w", err)
	}

	store := checkpoint.NewStore(cfg.CheckpointDir)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	return &DeepAgentRuntime{
		modelName:       cfg.DefaultModel,
		runner:          runner,
		maxHistoryTurns: 20,
		trace:           trace,
	}, nil
}

func (r *DeepAgentRuntime) Execute(ctx context.Context, prompt string) (Result, error) {
	return r.ExecuteStream(ctx, prompt, nil)
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
	msgs := make([]*schema.Message, len(r.history)+1)
	copy(msgs, r.history)
	msgs[len(msgs)-1] = schema.UserMessage(prompt)
	r.mu.Unlock()

	checkpointID := fmt.Sprintf("ckpt-%d", time.Now().UnixNano())
	iter := r.runner.Run(ctx, msgs, adk.WithCheckPointID(checkpointID))
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

func (r *DeepAgentRuntime) Name() string {
	if strings.TrimSpace(r.modelName) == "" {
		return "deep-agent"
	}
	return strings.TrimSpace(r.modelName)
}
