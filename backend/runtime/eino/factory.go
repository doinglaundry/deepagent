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
	if strings.TrimSpace(mc.Name) == "" {
		mc.Name = modelName
	}

	agentName := strings.TrimSpace(cfg.DefaultAgent)
	if agentName == "" {
		return nil, fmt.Errorf("default agent is required")
	}
	ac, ok := cfg.Agents[agentName]
	if !ok {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}

	switch strings.ToLower(strings.TrimSpace(mc.Provider)) {
	case "claude", "anthropic", "openai", "kimi", "moonshot":
		// Phase 5+: build the prompt-side data sources (skills / deferred
		// / ACP / memory) and the AppConfig view from the loaded YAML,
		// then thread them into NewDeepAgentRuntime alongside the runtime
		// extras (HITL approval, deferred-tool name resolver, memory
		// hooks).
		memoryStore := memorystore.NewStore(cfg.MemoryDir)
		memoryAcc := agent.NewMemoryAccessor(memoryStore)

		promptDeps := agent.BuildPromptDeps(cfg, agent.PromptDepsOptions{
			GetMemoryData:            memoryAcc.GetMemoryData,
			FormatMemoryForInjection: memoryAcc.FormatMemoryForInjection,
		})
		appCfg := agent.BuildAppConfig(cfg)
		extras := agent.RuntimeExtras{
			DeferredToolNames: agent.DeferredToolNamesFromConfig(cfg),
			HITLApproval:      defaultHITLApproval,
			HITLTools:         nil, // wired by REPL when /approve flow exists
			MemoryHooks:       memoryAcc.Hooks(),
			MemoryFlushHook:   memoryAcc.FlushBeforeSummarization,
		}
		return NewDeepAgentRuntime(ctx, *mc, ac, cfg.CheckpointDir, promptDeps, appCfg, extras)
	default:
		return nil, fmt.Errorf("unsupported model provider %q", mc.Provider)
	}
}
