package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
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
// All gates are read straight from cfg + rt — every host capability that
// used to live on agent.AgentDeps now has a self-contained default:
//   - Sandbox: NewLocalSandbox("") — cwd-backed local fs / shell.
//   - Mem: NewMemoryAccessor backed by cfg.MemoryDir (Memory middleware
//     is still gated on cfg.Memory.Enabled, so an empty MemoryDir
//     simply means "the gate stays off").
//   - HITL approval: defaultHITLApproval (stdin y/N), gated on rt.HITLTools.
//   - Deferred tool names: derived from cfg.ToolSearch.Deferred.
//
// MakeLeadAgent constructs its own sandbox / mem instances; BuildChain
// reconstructs equivalent ones here so each function stays callable in
// isolation. The instances are stateless (LocalSandbox) or read-only at
// this stage (MemoryAccessor's writes happen later, via the middleware
// hooks), so duplicate construction is free.
func BuildChain(
	ctx context.Context,
	rt RuntimeContext,
	cfg config.Config,
	summaryModel model.BaseChatModel,
) (Chain, error) {
	modelCfg := cfg.Models[rt.ModelName]
	sandbox := NewLocalSandbox("")
	mem := NewMemoryAccessor(memorystore.NewStore(cfg.MemoryDir))
	imageFetcher := discoverImageFetcher(sandbox)

	chatModel := []adk.ChatModelAgentMiddleware{
		middlewares.NewAgentState(),
		middlewares.NewTitle(),
		middlewares.NewToolErrorHandling(),
		middlewares.NewLoopDetection(),
	}

	if cfg.Memory.Enabled {
		chatModel = append(chatModel, middlewares.NewMemory(mem.Hooks()))
	}

	if cfg.TokenUsage.Enabled {
		chatModel = append(chatModel, middlewares.NewTokenUsage())
	}

	if modelCfg != nil && modelCfg.SupportsVision {
		chatModel = append(chatModel, middlewares.NewViewImage(imageFetcher))
	}

	if cfg.ToolSearch.Enabled {
		if names := DeferredToolNamesFromConfig(cfg); names != nil {
			chatModel = append(chatModel, middlewares.NewDeferredTools(names))
		}
	}

	if rt.SubagentEnabled {
		chatModel = append(chatModel, middlewares.NewSubagentLimit(rt.MaxConcurrentSubagents))
	}

	if len(rt.HITLTools) > 0 {
		chatModel = append(chatModel, middlewares.NewHITL(rt.HITLTools, defaultHITLApproval))
	}

	if cfg.Summarization.Enabled {
		summaryMW, err := middlewares.NewSummarization(
			ctx,
			cfg.Summarization.Enabled,
			0, // contextTokens — let the middleware default kick in
			0, // contextMessages — same
			cfg.Summarization.SummaryPrompt,
			summaryModel,
			mem.FlushBeforeSummarization,
		)
		if err != nil {
			return Chain{}, fmt.Errorf("build summarization mw: %w", err)
		}
		if summaryMW != nil {
			chatModel = append(chatModel, summaryMW)
		}
	}

	chatModel = append(chatModel, middlewares.NewClarification())

	var agentMWs []adk.AgentMiddleware
	if rt.IsPlanMode {
		agentMWs = append(agentMWs, middlewares.NewTodo())
	}

	return Chain{
		Agent:     agentMWs,
		ChatModel: chatModel,
	}, nil
}
