package eino

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/schema"
)

type DeepAgentRuntime struct {
	modelName string
	runner    *adk.Runner

	mu                  sync.Mutex
	pendingCheckpointID string
}

func NewDeepAgentRuntime(ctx context.Context, runtimeModel string, store adk.CheckPointStore) (Runtime, error) {
	modelCfg, err := getModelConfig(strings.TrimSpace(runtimeModel))
	if err != nil {
		return nil, err
	}

	chatModel, err := buildBaseChatModel(ctx, modelCfg)
	if err != nil {
		return nil, fmt.Errorf("build chat model: %w", err)
	}

	agent, err := deep.New(ctx, &deep.Config{
		Name:                   "deep-agent",
		Description:            "Deep Agent",
		ChatModel:              chatModel,
		Instruction:            "You are a helpful assistant.",
		ToolsConfig:            adk.ToolsConfig{},
		MaxIteration:           6,
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

	return &DeepAgentRuntime{modelName: modelCfg.Name, runner: runner}, nil
}

func (r *DeepAgentRuntime) Execute(ctx context.Context, prompt string) (Result, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Result{}, fmt.Errorf("prompt is required")
	}

	checkpointID := fmt.Sprintf("ckpt-%d", time.Now().UnixNano())
	iter := r.runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)}, adk.WithCheckPointID(checkpointID))
	summary, err := collectAgentEvents(iter)
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

	return SuccessResult(summary.Output), nil
}

func (r *DeepAgentRuntime) Name() string {
	if strings.TrimSpace(r.modelName) == "" {
		return "deep-agent"
	}
	return strings.TrimSpace(r.modelName)
}
