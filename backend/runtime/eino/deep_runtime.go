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
	// cfg / rt are the inputs needed to rebuild the lead agent when
	// SetPlanMode flips a runtime field; DeepAgentRuntime is the sole
	// owner of *RuntimeContext, all mutations go through setters under mu.
	cfg                 *config.Config
	rt                  *agent.RuntimeContext
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
func NewDeepAgentRuntime(ctx context.Context, cfg *config.Config) (Runtime, error) {
	rt, err := agent.NewRuntimeContext(cfg)
	if err != nil {
		return nil, err
	}

	leadAgent, trace, err := agent.MakeLeadAgent(ctx, rt, cfg)
	if err != nil {
		return nil, fmt.Errorf("build lead agent: %w", err)
	}

	store := checkpoint.NewStore(filepath.Join(cfg.RootDir, ".eino-cli", "checkpoints"))
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	return &DeepAgentRuntime{
		cfg:             cfg,
		rt:              rt,
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

// SetPlanMode flips IsPlanMode and rebuilds the lead agent + runner so the
// new system prompt and middleware list take effect on the next turn. No-op
// when the value is unchanged. On rebuild failure rt is rolled back so the
// existing runner / trace stay coherent. history is intentionally preserved
// — switching plan mode shouldn't wipe conversation context.
func (r *DeepAgentRuntime) SetPlanMode(ctx context.Context, plan bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rt.IsPlanMode == plan {
		return nil
	}

	r.rt.SetPlanMode(plan)

	leadAgent, trace, err := agent.MakeLeadAgent(ctx, r.rt, r.cfg)
	if err != nil {
		r.rt.SetPlanMode(!plan)
		return fmt.Errorf("rebuild lead agent for plan mode %v: %w", plan, err)
	}

	store := checkpoint.NewStore(filepath.Join(r.cfg.RootDir, ".eino-cli", "checkpoints"))
	r.runner = adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})
	r.trace = trace
	return nil
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
