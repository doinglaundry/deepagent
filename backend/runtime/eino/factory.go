package eino

import (
	"context"
	"fmt"
	"strings"

	"eino-cli/backend/agent"
	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

func BuildRuntime(ctx context.Context, cfg config.Config) (Runtime, error) {
	modelName := strings.TrimSpace(cfg.DefaultModel)
	if modelName == "" {
		return nil, fmt.Errorf("default model is required")
	}
	mc, ok := cfg.Models[modelName]
	if !ok {
		return nil, fmt.Errorf("model %q not found", modelName)
	}

	agentName := strings.TrimSpace(cfg.DefaultAgent)
	if agentName == "" {
		return nil, fmt.Errorf("default agent is required")
	}
	if _, ok := cfg.Agents[agentName]; !ok {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}

	switch strings.ToLower(strings.TrimSpace(mc.Provider)) {
	case "claude", "anthropic", "openai", "kimi", "moonshot":
	default:
		return nil, fmt.Errorf("unsupported model provider %q", mc.Provider)
	}

	memoryAcc := agent.NewMemoryAccessor(memorystore.NewStore(cfg.MemoryDir))

	deps := agent.AgentDeps{
		PromptDeps: agent.BuildPromptDeps(cfg, agent.PromptDepsOptions{
			GetMemoryData:            memoryAcc.GetMemoryData,
			FormatMemoryForInjection: memoryAcc.FormatMemoryForInjection,
		}),
		DeferredToolNames: agent.DeferredToolNamesFromConfig(cfg),
		HITLApproval:      defaultHITLApproval,
		MemoryHooks:       memoryAcc.Hooks(),
		MemoryFlushHook:   memoryAcc.FlushBeforeSummarization,
	}
	return NewDeepAgentRuntime(ctx, cfg, deps)
}
