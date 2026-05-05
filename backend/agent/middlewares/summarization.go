package middlewares

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
)

// NewSummarization wraps eino/adk/middlewares/summarization.New with our
// own defaults: trigger at 190k tokens or 200 messages, no transcript file
// (we inject one in Phase 4 once the sandbox layer can guarantee a writable
// path).
//
// The host calls this with primitive parameters so middlewares stays a leaf
// package and never imports agent (which would create a cycle).
//
// Returns (nil, nil) when enabled is false — the caller should simply skip
// appending the result to the chain in that case.
func NewSummarization(
	ctx context.Context,
	enabled bool,
	contextTokens, contextMessages int,
	userInstruction string,
	summaryModel model.BaseChatModel,
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

	mw, err := summarization.New(ctx, &summarization.Config{
		Model:           summaryModel,
		Trigger:         trig,
		UserInstruction: userInstruction,
	})
	if err != nil {
		return nil, fmt.Errorf("build summarization middleware: %w", err)
	}
	return mw, nil
}
