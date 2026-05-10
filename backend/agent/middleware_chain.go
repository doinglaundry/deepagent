package agent

import (
	"context"
	"log/slog"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"

	"github.com/cloudwego/eino/adk"
)

func GetChatModelMiddlewares(ctx context.Context, cfg *config.Config, mem *MemoryAccessor, rt RuntimeContext) (middlewareList []adk.ChatModelAgentMiddleware) {
	middlewareList = []adk.ChatModelAgentMiddleware{
		middlewares.NewAgentState(),
		middlewares.NewTitle(),
		middlewares.NewToolErrorHandling(),
		middlewares.NewLoopDetection(),
	}
	if cfg.Memory.Enabled {
		middlewareList = append(middlewareList, middlewares.NewMemory(mem.Hooks()))
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
		summaryMW, err := middlewares.NewSummarization(
			ctx,
			cfg.Summarization.Enabled,
			0,
			0,
			cfg.Summarization.SummaryPrompt,
			summaryModel,
			mem.FlushBeforeSummarization,
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
