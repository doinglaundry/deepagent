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
// promptDeps + appCfg are optional. When non-nil they populate the
// dynamic prompt sections (skills, deferred tools, ACP, memory hooks);
// when nil the prompt degrades to the same "no extras" output Phase 2
// shipped with.
//
// We keep the runtime's history/checkpoint/streaming responsibilities here
// because they belong to the eino-cli REPL, not to the agent itself.
func NewDeepAgentRuntime(
	ctx context.Context,
	modelCfg config.ModelConfig,
	agentCfg config.AgentConfig,
	checkpointDir string,
	promptDeps *agent.PromptDeps,
	appCfg *agent.AppConfig,
) (Runtime, error) {
	modelName := strings.TrimSpace(modelCfg.Name)
	if modelName == "" {
		modelName = strings.TrimSpace(modelCfg.Model)
	}
	if modelName == "" {
		return nil, fmt.Errorf("model name is required")
	}

	agentName := strings.TrimSpace(agentCfg.Name)
	if agentName == "" {
		agentName = "deep-agent"
	}

	// Build a single-entry config view for MakeLeadAgent. The REPL only
	// runs one agent + one model at a time, so this is a faithful
	// projection of the surrounding config.Config.
	cfgView := config.Config{
		DefaultModel: modelName,
		DefaultAgent: agentName,
		Models:       map[string]*config.ModelConfig{modelName: cloneModelCfg(modelCfg, modelName)},
		Agents:       map[string]config.AgentConfig{agentName: agentCfg},
	}

	rt := agent.NewRuntimeContext()
	rt.AgentName = ""        // empty → "default" branch in MakeLeadAgent
	rt.ModelName = modelName // resolve to the only model in cfgView
	rt.SubagentEnabled = false
	rt.IsPlanMode = false

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	deps := agent.AgentDeps{
		// Phase 4: the local sandbox provider replaces the previous
		// runtime/eino-owned newLocalBackend/newLocalShell helpers.
		Sandbox:    agent.NewLocalSandbox(cwd),
		WorkingDir: cwd,
		// Phase 5: nil values fall back to Python's "no extras" branches,
		// so callers that don't configure skills/deferred/ACP keep the
		// existing behaviour.
		PromptDeps: promptDeps,
		AppConfig:  appCfg,
	}

	leadAgent, err := agent.MakeLeadAgent(ctx, rt, cfgView, deps)
	if err != nil {
		return nil, fmt.Errorf("build lead agent: %w", err)
	}

	store := checkpoint.NewStore(checkpointDir)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	return &DeepAgentRuntime{modelName: modelName, runner: runner, maxHistoryTurns: 20}, nil
}

func cloneModelCfg(in config.ModelConfig, defaultName string) *config.ModelConfig {
	out := in
	if strings.TrimSpace(out.Name) == "" {
		out.Name = defaultName
	}
	return &out
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
