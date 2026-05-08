package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

// Chain bundles the two middleware slices that deep.New consumes.
//
// Python's _build_middlewares returns a single list because LangChain only
// has one middleware abstraction. eino splits the same surface into two
// slots: AgentMiddleware (struct-based, simple instruction/tool extras) and
// ChatModelAgentMiddleware (interface-based, full hook surface). We expose
// both so each Phase can drop the right one in.
type Chain struct {
	Agent     []adk.AgentMiddleware
	ChatModel []adk.ChatModelAgentMiddleware
}

// BuildChain mirrors Python _build_middlewares. The slot order matches the
// deerflow code in spirit:
//
//	always-on   → AgentState, Title, ToolError, LoopDetect
//	gated       → TokenUsage, ViewImage, DeferredTools, SubagentLimit,
//	              Memory, HITL, Summarize  (each behind its YAML flag)
//	always-last → Clarification (Python invariant)
//
// Plus the AgentMiddleware (struct-based) slot for plan-mode Todo, since
// AgentMiddleware is the natural fit for static instruction additions.
//
// Gates are read straight from cfg — there is no longer a separate
// agent.AppConfig view. HITL is gated on `len(deps.HITLTools) > 0`
// alone (the deer-flow yaml gate had no independent meaning).
func BuildChain(
	ctx context.Context,
	rt RuntimeContext,
	cfg config.Config,
	deps AgentDeps,
	summaryModel model.BaseChatModel,
) (Chain, error) {
	modelCfg := cfg.Models[rt.ModelName]
	imageFetcher := discoverImageFetcher(deps.Sandbox)

	chatModel := []adk.ChatModelAgentMiddleware{
		middlewares.NewAgentState(),
		middlewares.NewTitle(),
		middlewares.NewToolErrorHandling(),
		middlewares.NewLoopDetection(),
	}

	if cfg.Memory.Enabled {
		chatModel = append(chatModel, middlewares.NewMemory(deps.MemoryHooks))
	}

	if cfg.TokenUsage.Enabled {
		chatModel = append(chatModel, middlewares.NewTokenUsage())
	}

	if modelCfg != nil && modelCfg.SupportsVision {
		chatModel = append(chatModel, middlewares.NewViewImage(imageFetcher))
	}

	if cfg.ToolSearch.Enabled && deps.DeferredToolNames != nil {
		chatModel = append(chatModel, middlewares.NewDeferredTools(deps.DeferredToolNames))
	}

	if rt.SubagentEnabled {
		chatModel = append(chatModel, middlewares.NewSubagentLimit(rt.MaxConcurrentSubagents))
	}

	if len(deps.HITLTools) > 0 {
		chatModel = append(chatModel, middlewares.NewHITL(deps.HITLTools, deps.HITLApproval))
	}

	if cfg.Summarization.Enabled {
		summaryMW, err := middlewares.NewSummarization(
			ctx,
			cfg.Summarization.Enabled,
			0, // contextTokens — let the middleware default kick in
			0, // contextMessages — same
			cfg.Summarization.SummaryPrompt,
			summaryModel,
			deps.MemoryFlushHook,
		)
		if err != nil {
			return Chain{}, fmt.Errorf("build summarization mw: %w", err)
		}
		if summaryMW != nil {
			chatModel = append(chatModel, summaryMW)
		}
	}

	clar := middlewares.NewClarification()
	clar.OnQuestion = deps.OnClarification
	chatModel = append(chatModel, clar)

	var agentMWs []adk.AgentMiddleware
	if rt.IsPlanMode {
		agentMWs = append(agentMWs, middlewares.NewTodo())
	}

	return Chain{
		Agent:     agentMWs,
		ChatModel: chatModel,
	}, nil
}
