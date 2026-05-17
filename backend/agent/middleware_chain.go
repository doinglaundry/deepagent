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
) (middlewareList []adk.ChatModelAgentMiddleware) {
	patchToolCalls, _ := patchtoolcalls.New(ctx, nil)

	middlewareList = []adk.ChatModelAgentMiddleware{
		middlewares.NewAgentState(),
		middlewares.NewToolCallObservability(cfg.ToolObservability.Enabled),
		middlewares.NewToolErrorHandling(),
		patchToolCalls,
		middlewares.NewLoopDetection(),
	}

	var (
		store   = memorystore.NewStoreFromConfig(cfg)
		updater = memory.NewMemoryUpdater(store)
	)

	if cfg.Memory.Enabled {
		middlewareList = append(middlewareList, middlewares.NewMemory(middlewares.MemoryHooks{
			Inject: func(_ context.Context, msgs []*schema.Message) []*schema.Message {
				return memory.InjectMemory(store, cfg.Memory, agentName, msgs)
			},
			Extract: func(ctx context.Context, msgs []*schema.Message) {
				err := updater.Run(ctx, chatModel, cfg.Memory, agentName, msgs, false)
				if err != nil {
					slog.Warn("memory update failed", "agent", agentName, "err", err)
				}
			},
		}))
	}

	var tokenUsage *middlewares.TokenUsage
	if cfg.TokenUsage.Enabled {
		tokenUsage = middlewares.NewTokenUsage()
		middlewareList = append(middlewareList, tokenUsage)
	}

	if cfg.ToolSearch.Enabled {
		if names := DeferredToolNamesFromConfig(cfg); names != nil {
			middlewareList = append(middlewareList, middlewares.NewDeferredTools(names))
		}
	}

	if isSubagentEnabled {
		middlewareList = append(middlewareList, middlewares.NewSubagentLimit(effectiveMaxSubagents(cfg)))
	}

	if len(cfg.HITLTools) > 0 {
		middlewareList = append(middlewareList, middlewares.NewHITL(cfg.HITLTools, HITLApprover))
	}

	if cfg.Summarization.Enabled {
		summaryMW, err := middlewares.NewSummarization(ctx, cfg, updater, buildSummaryChatModel(ctx, cfg))
		if err == nil {
			middlewareList = append(middlewareList, summaryMW)
		}
	}

	middlewareList = append(middlewareList, middlewares.NewPlanReminder(getPlanModeFunc))

	middlewareList = append(middlewareList, middlewares.NewTodoReminder())

	// Sandbox before Trace; the Trace→Clarification invariant is asserted in middleware_chain_test.go.
	middlewareList = append(middlewareList, middlewares.NewSandbox(sandbox.Default()))

	trace := middlewares.NewTrace(agentName)
	if tokenUsage != nil {
		trace.TokenSnapshot = tokenUsage.Snapshot
	}
	middlewareList = append(middlewareList, trace)
	middlewareList = append(middlewareList, middlewares.NewClarification())
	return
}
