package agent

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

// GetChatModelMiddlewares assembles the chat-model middleware chain. Memory
// store and updater are constructed here (and only here) so MakeLeadAgent
// stays memory-agnostic; the same updater instance is shared between the
// memory hook and the summarization flush hook so debounce state is honoured
// across both code paths.
func GetChatModelMiddlewares(
	ctx context.Context,
	cfg *config.Config,
	rt RuntimeContext,
	chatModel model.BaseChatModel,
) (middlewareList []adk.ChatModelAgentMiddleware) {
	middlewareList = []adk.ChatModelAgentMiddleware{
		middlewares.NewAgentState(),
		middlewares.NewTitle(),
		middlewares.NewToolErrorHandling(),
		middlewares.NewLoopDetection(),
	}

	// store/updater must be visible to both the memory branch and the
	// summarization branch below; declaring them here keeps the wiring
	// explicit. updater stays nil when memory is disabled, which the
	// flush hook short-circuits on.
	var (
		store   *memorystore.Store
		updater *MemoryUpdater
	)
	if cfg.Memory.Enabled {
		store = memorystore.NewStoreFromConfig(cfg)
		updater = NewMemoryUpdater(store)

		hooks := middlewares.MemoryHooks{
			Inject: func(_ context.Context, msgs []*schema.Message) []*schema.Message {
				return InjectMemory(store, cfg.Memory, rt.AgentName, msgs)
			},
			Extract: func(ctx context.Context, msgs []*schema.Message) {
				err := updater.Run(ctx, chatModel, cfg.Memory, rt.AgentName, msgs, false)
				if err != nil {
					slog.Warn("memory update failed", "agent", rt.AgentName, "err", err)
				}
			},
		}
		middlewareList = append(middlewareList, middlewares.NewMemory(hooks))
	}

	if cfg.TokenUsage.Enabled {
		middlewareList = append(middlewareList, middlewares.NewTokenUsage())
	}

	if cfg.ToolSearch.Enabled {
		if names := DeferredToolNamesFromConfig(cfg); names != nil {
			middlewareList = append(middlewareList, middlewares.NewDeferredTools(names))
		}
	}

	if rt.SubagentEnabled {
		middlewareList = append(middlewareList, middlewares.NewSubagentLimit(rt.MaxConcurrentSubagents))
	}

	if len(rt.HITLTools) > 0 {
		middlewareList = append(middlewareList, middlewares.NewHITL(rt.HITLTools, defaultHITLApproval))
	}

	if cfg.Summarization.Enabled {
		summaryModel := buildSummaryChatModel(ctx, cfg)

		// flushHook reuses the same updater instance as the memory hook so
		// lastRunAt is shared; updater==nil (memory disabled) means there's
		// nothing to flush, which the closure handles up front.
		flushHook := func(ctx context.Context, before, _ adk.ChatModelAgentState) error {
			if updater == nil {
				return nil
			}
			flushCtx, cancel := context.WithTimeout(ctx, memoryFlushTimeout)
			defer cancel()
			return updater.Run(flushCtx, chatModel, cfg.Memory, rt.AgentName, before.Messages, true)
		}

		summaryMW, err := middlewares.NewSummarization(
			ctx,
			cfg.Summarization.Enabled,
			0,
			0,
			cfg.Summarization.SummaryPrompt,
			summaryModel,
			flushHook,
		)
		if err != nil {
			slog.Warn("summarization disabled: build failed", "err", err)
		} else if summaryMW != nil {
			middlewareList = append(middlewareList, summaryMW)
		}
	}

	// Trace must run BEFORE Clarification so its After hook captures
	// the raw assistant message before Clarification's in-place rewrite.
	middlewareList = append(middlewareList, middlewares.NewTrace(rt.AgentName))
	middlewareList = append(middlewareList, middlewares.NewClarification())
	return
}

func GetAgentMiddleWares(rt RuntimeContext) (res []adk.AgentMiddleware) {
	res = make([]adk.AgentMiddleware, 0)
	if rt.IsPlanMode {
		res = append(res, middlewares.NewTodo())
	}
	return
}
