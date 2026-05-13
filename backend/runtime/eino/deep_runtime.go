package eino

import (
	"context"
	"fmt"
	"path/filepath"
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
	// cfg is kept for ClearHistory's checkpoint path; every other knob
	// is a function argument to agent.MakeLeadAgent at construction time.
	cfg                 *config.Config
	modelName           string
	runner              *adk.Runner
	mu                  sync.Mutex
	pendingCheckpointID string
	history             []*schema.Message
	maxHistoryTurns     int
	// trace is the lead agent's debug-trace; nil-safe; used only by ClearHistory.
	trace *middlewares.Trace
}

// NewDeepAgentRuntime builds the lead agent and the adk.Runner; history /
// checkpoint / streaming live here (REPL-owned).
func NewDeepAgentRuntime(ctx context.Context, cfg *config.Config) (Runtime, error) {
	runner, trace, err := buildLeadRunner(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return &DeepAgentRuntime{
		cfg:             cfg,
		modelName:       cfg.DefaultModel,
		runner:          runner,
		maxHistoryTurns: 20,
		trace:           trace,
	}, nil
}

func buildLeadRunner(ctx context.Context, cfg *config.Config) (*adk.Runner, *middlewares.Trace, error) {
	leadAgent, trace, err := agent.MakeLeadAgent(ctx, "default", false, true, cfg)
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

func (r *DeepAgentRuntime) Execute(ctx context.Context, prompt string) (Result, error) {
	return r.ExecuteStream(ctx, prompt, nil)
}

func (r *DeepAgentRuntime) ExecuteStream(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Result{}, fmt.Errorf("prompt is required")
	}

	// Snapshot runner under the same lock that protects history + r.runner,
	// so SetPlanMode swapping r.runner doesn't race with ExecuteStream.
	r.mu.Lock()
	if len(r.history) > r.maxHistoryTurns*2 {
		r.history = r.history[len(r.history)-r.maxHistoryTurns*2:]
	}
	msgs := make([]*schema.Message, len(r.history)+1)
	copy(msgs, r.history)
	msgs[len(msgs)-1] = schema.UserMessage(prompt)
	runner := r.runner
	r.mu.Unlock()

	checkpointID := fmt.Sprintf("ckpt-%d", time.Now().UnixNano())
	iter := runner.Run(ctx, msgs, adk.WithCheckPointID(checkpointID))
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

func (r *DeepAgentRuntime) ReloadSoul(ctx context.Context) error {
	runner, trace, err := buildLeadRunner(ctx, r.cfg)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.runner = runner
	r.trace = trace
	r.history = nil
	r.pendingCheckpointID = ""
	r.mu.Unlock()
	if trace != nil {
		trace.ResetTurn()
	}
	return nil
}

func (r *DeepAgentRuntime) Name() string {
	if strings.TrimSpace(r.modelName) == "" {
		return "deep-agent"
	}
	return strings.TrimSpace(r.modelName)
}
