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

// ChainOptions bundles everything BuildChain needs to assemble the
// middleware slices. Keeping it as a struct (instead of a long parameter
// list) lets future phases add knobs without touching every call site.
type ChainOptions struct {
	Runtime      RuntimeContext
	ModelName    string
	AgentName    string
	ModelConfig  *config.ModelConfig
	Config       config.Config
	AppConfig    *AppConfig // may be nil; behaves as zero-value
	SummaryModel model.BaseChatModel

	// MemoryHooks plugs the host-side memory data plane into the Memory
	// middleware. Optional; without hooks the middleware is a no-op even
	// when the gate flag is on.
	MemoryHooks middlewares.MemoryHooks

	// DeferredToolNames provides the live deferred-tool name list that the
	// DeferredTools middleware filters out of the active tool set. Wire
	// this from the same source as PromptDeps.GetDeferredRegistry.
	DeferredToolNames func() []string

	// HITLApproval is consulted for each gated tool call when HITL is
	// enabled. Nil treats every call as approved.
	HITLApproval func(ctx context.Context, toolName, args string) bool

	// HITLTools are the tool names that require approval (e.g. shell.execute).
	HITLTools []string

	// OnClarification (optional) is called whenever the Clarification
	// middleware rewrites an ask_clarification tool call. The host can
	// use this for telemetry / custom rendering — the rewrite happens
	// regardless.
	OnClarification func(ctx context.Context, question string)

	// ImageFetcher provides the binary image bytes the ViewImage
	// middleware needs to construct multimodal user messages. Without
	// it the middleware degrades to logging skeleton even when the
	// active model SupportsVision.
	ImageFetcher middlewares.ImageFetcher
}

// BuildChain mirrors Python _build_middlewares. The slot order matches the
// deerflow code in spirit:
//
//	always-on   → AgentState, Title, ToolError, LoopDetect
//	gated       → TokenUsage, ViewImage, DeferredTools, SubagentLimit,
//	              Memory, HITL, Summarize  (each behind its config flag)
//	always-last → Clarification (Python invariant)
//
// Plus the AgentMiddleware (struct-based) slot for plan-mode Todo, since
// AgentMiddleware is the natural fit for static instruction additions.
func BuildChain(ctx context.Context, opts ChainOptions) (Chain, error) {
	app := opts.AppConfig

	chatModel := []adk.ChatModelAgentMiddleware{
		middlewares.NewAgentState(),
		middlewares.NewTitle(),
		middlewares.NewToolErrorHandling(),
		middlewares.NewLoopDetection(),
	}

	if app != nil && app.Memory.Enabled {
		chatModel = append(chatModel, middlewares.NewMemory(opts.MemoryHooks))
	}

	if app != nil && app.TokenUsage.Enabled {
		chatModel = append(chatModel, middlewares.NewTokenUsage())
	}

	if opts.ModelConfig != nil && opts.ModelConfig.SupportsVision {
		chatModel = append(chatModel, middlewares.NewViewImage(opts.ImageFetcher))
	}

	if app != nil && app.ToolSearch.Enabled && opts.DeferredToolNames != nil {
		chatModel = append(chatModel, middlewares.NewDeferredTools(opts.DeferredToolNames))
	}

	if opts.Runtime.SubagentEnabled {
		chatModel = append(chatModel, middlewares.NewSubagentLimit(opts.Runtime.MaxConcurrentSubagents))
	}

	if app != nil && app.HumanInTheLoop.Enabled && len(opts.HITLTools) > 0 {
		chatModel = append(chatModel, middlewares.NewHITL(opts.HITLTools, opts.HITLApproval))
	}

	if app != nil && app.Summarization.Enabled {
		summaryMW, err := middlewares.NewSummarization(
			ctx,
			app.Summarization.Enabled,
			app.Summarization.ContextTokens,
			app.Summarization.ContextMessages,
			app.Summarization.UserInstruction,
			opts.SummaryModel,
		)
		if err != nil {
			return Chain{}, fmt.Errorf("build summarization mw: %w", err)
		}
		if summaryMW != nil {
			chatModel = append(chatModel, summaryMW)
		}
	}

	// Clarification stays last — same invariant as Python.
	clar := middlewares.NewClarification()
	clar.OnQuestion = opts.OnClarification
	chatModel = append(chatModel, clar)

	var agentMWs []adk.AgentMiddleware
	if opts.Runtime.IsPlanMode {
		agentMWs = append(agentMWs, middlewares.NewTodo())
	}

	return Chain{
		Agent:     agentMWs,
		ChatModel: chatModel,
	}, nil
}
