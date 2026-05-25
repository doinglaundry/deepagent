package deepagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent"
	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	"eino-cli/backend/consts"
	rt "eino-cli/backend/runtime"
	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/session/checkpoint"
)

type Runtime struct {
	cfg                 *config.Config
	modelName           string
	runner              *adk.Runner
	mu                  sync.Mutex
	pendingCheckpointID string
	history             []*schema.Message
	maxHistoryTurns     int
	trace               *middlewares.Trace
	planMode            atomic.Bool
	autoDreamState      autoDreamState
}

func NewRuntime(ctx context.Context, cfg *config.Config) (rt.Runtime, error) {
	r := &Runtime{
		cfg:             cfg,
		modelName:       cfg.DefaultModel,
		maxHistoryTurns: 20,
	}
	leadAgent, trace, err := agent.MakeLeadAgent(ctx, true, r.planMode.Load, cfg)
	if err != nil {
		return nil, fmt.Errorf("build lead agent: %w", err)
	}
	r.runner = adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
		CheckPointStore: checkpoint.NewStore(config.SessionCheckpointsDir(consts.DefaultSessionID)),
	})
	r.trace = trace
	return r, nil
}

func (r *Runtime) ExecuteStream(ctx context.Context, prompt string, onChunk rt.StreamChunkHandler) (rt.Result, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return rt.Result{}, fmt.Errorf("prompt is required")
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

	if runtimecontext.GetSessionID(ctx) == "" {
		ctx = runtimecontext.WithSessionID(ctx, consts.DefaultSessionID)
	}

	checkpointID := fmt.Sprintf("ckpt-%d", time.Now().UnixNano())
	iter := runner.Run(ctx, messages, adk.WithCheckPointID(checkpointID))
	summary, err := collectAgentEventsWithSink(iter, onChunk)
	if err != nil {
		return rt.Result{}, err
	}

	if summary.Interrupted {
		r.mu.Lock()
		r.pendingCheckpointID = checkpointID
		r.mu.Unlock()
		return rt.Result{Success: false, Code: rt.ErrorCodeRuntime, Message: "execution interrupted", NeedsUser: true}, nil
	}

	if strings.TrimSpace(summary.Output) == "" {
		return rt.Result{}, fmt.Errorf("deep runtime returned empty output")
	}

	r.mu.Lock()
	r.history = append(r.history, schema.UserMessage(prompt), schema.AssistantMessage(summary.Output, nil))
	r.mu.Unlock()

	return rt.Result{Success: true, Output: summary.Output}, nil
}

func (r *Runtime) ClearHistory() {
	r.mu.Lock()
	r.history = nil
	r.mu.Unlock()
	if r.trace != nil {
		r.trace.ResetTurn()
	}
}

func (r *Runtime) ExportHistory() ([]byte, error) {
	r.mu.Lock()
	history := append([]*schema.Message(nil), r.history...)
	r.mu.Unlock()
	payload, err := json.Marshal(history)
	if err != nil {
		return nil, fmt.Errorf("marshal runtime history: %w", err)
	}
	return payload, nil
}

func (r *Runtime) ImportHistory(payload []byte) error {
	var history []*schema.Message
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &history); err != nil {
			return fmt.Errorf("decode runtime history: %w", err)
		}
	}
	r.mu.Lock()
	r.history = append([]*schema.Message(nil), history...)
	r.mu.Unlock()
	return nil
}

func (r *Runtime) RollbackToHistory(payload []byte) error {
	if err := r.ImportHistory(payload); err != nil {
		return err
	}
	if r.trace != nil {
		r.trace.ResetTurn()
	}
	return nil
}

func (r *Runtime) SetPlanMode(_ context.Context, on bool) (bool, error) {
	r.planMode.Store(on)
	return on, nil
}

func (r *Runtime) Name() string {
	if strings.TrimSpace(r.modelName) == "" {
		return "deep-agent"
	}
	return strings.TrimSpace(r.modelName)
}
