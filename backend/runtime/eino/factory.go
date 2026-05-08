package eino

import (
	"context"
	"fmt"
	"strings"

	"eino-cli/backend/agent"
	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

// BuildRuntime is the entry point that turns a fully-loaded config
// into a Runtime. config.Load() has already established every
// invariant we need (default model + agent exist, Models map is
// populated), so the only validation that lives here is the one
// invariant Load can't speak to: this package only knows how to
// drive a fixed set of model providers.
func BuildRuntime(ctx context.Context, cfg config.Config) (Runtime, error) {
	mc := cfg.Models[cfg.DefaultModel]
	switch strings.ToLower(strings.TrimSpace(mc.Provider)) {
	case "claude", "anthropic", "openai", "kimi", "moonshot":
	default:
		return nil, fmt.Errorf("unsupported model provider %q", mc.Provider)
	}

	memoryAcc := agent.NewMemoryAccessor(memorystore.NewStore(cfg.MemoryDir))
	deps := agent.AgentDeps{
		PromptDeps:            agent.BuildPromptDeps(cfg, memoryAcc),
		DeferredToolNamesFunc: agent.DeferredToolNamesFromConfig(cfg),
		HITLApprovalFunc:      defaultHITLApproval,
		MemoryHooks:           memoryAcc.Hooks(),
		MemoryFlushHookFunc:   memoryAcc.FlushBeforeSummarization,
	}
	return NewDeepAgentRuntime(ctx, cfg, deps)
}
