package middlewares

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
)

// SummarizationMemoryFlushHook fires once per summarization trigger, after
// the summary is finalised. Errors are surfaced as warnings only — flushing
// memory must never block summarization.
type SummarizationMemoryFlushHook func(ctx context.Context, before, after adk.ChatModelAgentState) error

// NewSummarization wraps eino's summarization middleware with eino-cli defaults
// (190k token / 200-message trigger). Returns (nil, nil) when enabled is false.
func NewSummarization(
	ctx context.Context,
	enabled bool,
	contextTokens, contextMessages int,
	userInstruction string,
	summaryModel model.BaseChatModel,
	memoryFlush SummarizationMemoryFlushHook,
) (adk.ChatModelAgentMiddleware, error) {
	if !enabled {
		return nil, nil
	}
	if summaryModel == nil {
		return nil, fmt.Errorf("summarization enabled but no chat model provided")
	}

	trig := &summarization.TriggerCondition{
		ContextTokens:   190000,
		ContextMessages: 200,
	}
	if contextTokens > 0 {
		trig.ContextTokens = contextTokens
	}
	if contextMessages > 0 {
		trig.ContextMessages = contextMessages
	}

	cfg := &summarization.Config{
		Model:           summaryModel,
		Trigger:         trig,
		UserInstruction: userInstruction,
	}
	if memoryFlush != nil {
		cfg.Callback = summarization.CallbackFunc(memoryFlush)
	}

	mw, err := summarization.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build summarization middleware: %w", err)
	}
	return mw, nil
}
