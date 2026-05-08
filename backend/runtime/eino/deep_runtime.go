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
}

// NewDeepAgentRuntime delegates the actual agent construction to
// agent.MakeLeadAgent — that's the entry point that runs the ported
// deerflow lead-agent assembly (prompt template, middleware chain,
// model resolution).
//
// cfg is trusted to satisfy the post-Load invariants (default model
// + agent exist, Models / Agents maps populated). agent.MakeLeadAgent
// owns its sandbox and memory accessor, so this function reduces to:
//
//  1. Stand up an empty RuntimeContext seeded with the cfg defaults.
//  2. Hand it to agent.MakeLeadAgent.
//  3. Wrap the resulting lead agent in an adk.Runner.
//
// We keep the runtime's history/checkpoint/streaming responsibilities
// here because they belong to the eino-cli REPL, not to the agent
// itself.
func NewDeepAgentRuntime(ctx context.Context, cfg config.Config) (Runtime, error) {
	rt := agent.NewRuntimeContext()
	rt.AgentName = cfg.DefaultAgent
	rt.ModelName = cfg.DefaultModel
	rt.SubagentEnabled = false
	rt.IsPlanMode = false

	leadAgent, err := agent.MakeLeadAgent(ctx, rt, cfg)
	if err != nil {
		return nil, fmt.Errorf("build lead agent: %w", err)
	}

	store := checkpoint.NewStore(cfg.CheckpointDir)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	return &DeepAgentRuntime{modelName: cfg.DefaultModel, runner: runner, maxHistoryTurns: 20}, nil
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
}

func (r *DeepAgentRuntime) Name() string {
	if strings.TrimSpace(r.modelName) == "" {
		return "deep-agent"
	}
	return strings.TrimSpace(r.modelName)
}
