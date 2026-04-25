package eino

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"eino-cli/internal/config"
)

type DeepAgentRuntime struct {
	modelName           string
	runner              *adk.Runner
	mu                  sync.Mutex
	pendingCheckpointID string
	history             []*schema.Message
	maxHistoryTurns     int
}

func NewDeepAgentRuntime(ctx context.Context, modelCfg config.ModelConfig, agentCfg config.AgentConfig, store adk.CheckPointStore) (Runtime, error) {
	chatModel, err := buildBaseChatModel(ctx, modelCfg)
	if err != nil {
		return nil, fmt.Errorf("build chat model: %w", err)
	}

	agentName := strings.TrimSpace(agentCfg.Name)
	if agentName == "" {
		agentName = "deep-agent"
	}
	instruction := strings.TrimSpace(agentCfg.Instruction)
	if instruction == "" {
		instruction = "You are a helpful assistant."
	}
	maxIteration := agentCfg.MaxIteration
	if maxIteration <= 0 {
		maxIteration = 6
	}

	runtimeTools := buildRuntimeTools()
	agent, err := deep.New(ctx, &deep.Config{
		Name:        agentName,
		Description: "Deep Agent",
		ChatModel:   chatModel,
		Instruction: instruction,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: runtimeTools,
		}},
		MaxIteration:           maxIteration,
		WithoutGeneralSubAgent: true,
		WithoutWriteTodos:      true,
	})
	if err != nil {
		return nil, fmt.Errorf("build deep agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	modelName := strings.TrimSpace(modelCfg.Name)
	if modelName == "" {
		modelName = strings.TrimSpace(modelCfg.Model)
	}
	maxTurns := agentCfg.MaxHistoryTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}
	return &DeepAgentRuntime{modelName: modelName, runner: runner, maxHistoryTurns: maxTurns}, nil
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
