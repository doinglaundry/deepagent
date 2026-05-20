package agent

import (
	"context"
	"eino-cli/backend/agent/memory"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
	"eino-cli/backend/sandbox"
)

func GetChatModelMiddlewares(
	ctx context.Context,
	agentName string,
	isSubagentEnabled bool,
	getPlanModeFunc func() bool,
	cfg *config.Config,
	chatModel model.BaseChatModel,
	sandboxManager sandbox.SandboxManager,
) (middlewareList []adk.ChatModelAgentMiddleware) {
	patchToolCalls, _ := patchtoolcalls.New(ctx, nil)

	middlewareList = []adk.ChatModelAgentMiddleware{
		middlewares.NewAgentState(),
		middlewares.NewToolCallObservability(true),
		middlewares.NewToolErrorHandling(),
		patchToolCalls,
		middlewares.NewLoopDetection(),
	}

	var (
		store   = memorystore.NewStoreFromConfig()
		updater = memory.NewMemoryUpdater(store)
		memCfg  = getDefaultMemoryConfig()
	)

	middlewareList = append(middlewareList, middlewares.NewMemory(middlewares.MemoryHooks{
		Inject: func(_ context.Context, msgs []*schema.Message) []*schema.Message {
			return memory.InjectMemory(store, memCfg, agentName, msgs)
		},
		Extract: func(ctx context.Context, msgs []*schema.Message) {
			err := updater.Run(ctx, chatModel, memCfg, agentName, msgs, false)
			if err != nil {
				slog.Warn("memory update failed", "agent", agentName, "err", err)
			}
		},
	}))

	var tokenUsage *middlewares.TokenUsage
	tokenUsage = middlewares.NewTokenUsage()
	middlewareList = append(middlewareList, tokenUsage)

	if names := defaultDeferredToolNames(); names != nil {
		middlewareList = append(middlewareList, middlewares.NewDeferredTools(names))
	}

	if isSubagentEnabled {
		middlewareList = append(middlewareList, middlewares.NewSubagentLimit(getMaxSubagents()))
	}

	middlewareList = append(middlewareList, middlewares.NewHITL(getDefaultHITLTools(), HITLApprover))

	summaryMW, err := middlewares.NewSummarization(ctx, memCfg, updater, buildSummaryChatModel(ctx, cfg))
	if err == nil {
		middlewareList = append(middlewareList, summaryMW)
	}

	middlewareList = append(middlewareList, middlewares.NewPlanReminder(getPlanModeFunc))

	middlewareList = append(middlewareList, middlewares.NewTodoReminder())

	// Sandbox before Trace; the Trace→Clarification invariant is asserted in middleware_chain_test.go.
	middlewareList = append(middlewareList, middlewares.NewSandbox(sandboxManager))
	middlewareList = append(middlewareList, middlewares.NewMessagesLog(config.AgentMessagesLogPath()))

	trace := middlewares.NewTrace(agentName)
	if tokenUsage != nil {
		trace.TokenSnapshot = tokenUsage.Snapshot
	}
	middlewareList = append(middlewareList, trace)
	middlewareList = append(middlewareList, middlewares.NewClarification())
	return
}

func getDefaultMemoryConfig() config.Memory {
	return config.Memory{
		Enabled:                   true,
		InjectionEnabled:          true,
		DebounceSeconds:           60,
		MaxInjectionTokens:        2000,
		MaxFacts:                  100,
		FactConfidenceThreshold:   0.7,
		DedupEnabled:              true,
		EpisodicDefaultTTLSeconds: 30 * 24 * 60 * 60,
	}
}

func defaultDeferredToolNames() []string {
	return nil
}

func getDefaultHITLTools() []string {
	return []string{
		"write_file",
		"edit_file",
		"delete_file",
		"apply_patch",
		"execute",
		"shell",
	}
}
