package eino

import (
	"context"
	"fmt"
	"os"
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
// cfg is the full loaded config; deps carries the host-supplied extras
// (sandbox, prompt deps, app config, HITL/memory hooks). Both the
// model and agent names come from cfg.DefaultModel / cfg.DefaultAgent
// — BuildRuntime validates those upstream, so this layer trusts cfg
// and only fills in missing host-context defaults (cwd-backed
// sandbox).
//
// We keep the runtime's history/checkpoint/streaming responsibilities
// here because they belong to the eino-cli REPL, not to the agent
// itself.
func NewDeepAgentRuntime(ctx context.Context, cfg config.Config, deps agent.AgentDeps) (Runtime, error) {
	modelName := strings.TrimSpace(cfg.DefaultModel)
	if modelName == "" {
		return nil, fmt.Errorf("model name is required")
	}
	if _, ok := cfg.Models[modelName]; !ok {
		return nil, fmt.Errorf("model %q not found", modelName)
	}

	agentName := strings.TrimSpace(cfg.DefaultAgent)
	if agentName == "" {
		agentName = "deep-agent"
	}

	rt := agent.NewRuntimeContext()
	rt.AgentName = agentName
	rt.ModelName = modelName
	rt.SubagentEnabled = false
	rt.IsPlanMode = false

	if deps.Sandbox == nil || deps.WorkingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		if deps.Sandbox == nil {
			deps.Sandbox = agent.NewLocalSandbox(cwd)
		}
		if deps.WorkingDir == "" {
			deps.WorkingDir = cwd
		}
	}

	leadAgent, err := agent.MakeLeadAgent(ctx, rt, cfg, deps)
	if err != nil {
		return nil, fmt.Errorf("build lead agent: %w", err)
	}

	store := checkpoint.NewStore(cfg.CheckpointDir)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	return &DeepAgentRuntime{modelName: modelName, runner: runner, maxHistoryTurns: 20}, nil
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
