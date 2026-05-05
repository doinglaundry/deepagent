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
	Config       config.Config
	AppConfig    *AppConfig // may be nil; behaves as zero-value
	SummaryModel model.BaseChatModel
}

// BuildChain mirrors Python _build_middlewares. The slot order matches the
// deerflow code in spirit:
//
//  1. AgentState  — always-on counters, must be early so others observe.
//  2. Title       — always-on first-user-message hook.
//  3. ToolError   — always-on tool-exception trap.
//  4. LoopDetect  — always-on duplicate tool-call detector.
//  5. Summarize   — gated by AppConfig.Summarization.Enabled.
//  6. Clarify     — always-LAST per the Python "should always be last" rule.
//
// Phase 3 will splice the gated middlewares (TokenUsage, ViewImage,
// DeferredTools, SubagentLimit, Todo, Memory, HITL) into slots 4.5 and 5.5.
func BuildChain(ctx context.Context, opts ChainOptions) (Chain, error) {
	app := opts.AppConfig

	chatModel := []adk.ChatModelAgentMiddleware{
		middlewares.NewAgentState(),
		middlewares.NewTitle(),
		middlewares.NewToolErrorHandling(),
		middlewares.NewLoopDetection(),
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
	chatModel = append(chatModel, middlewares.NewClarification())

	return Chain{
		Agent:     nil,
		ChatModel: chatModel,
	}, nil
}
