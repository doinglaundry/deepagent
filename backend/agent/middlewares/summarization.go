package middlewares

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
)

// SummarizationMemoryFlushHook is invoked exactly once per summarization
// trigger, after the model has produced a summary and before the
// middleware returns. Hosts plug the memory subsystem in here to
// persist any pending memories before the original messages are
// compacted out — mirrors deerflow's `memory_flush_hook` callback wired
// into `BeforeSummarization` (Python) or `Callback` (here).
//
// The hook receives the snapshot of agent state both before and after
// summarization so memory implementations can inspect what's about to
// be discarded. Errors are surfaced as warnings only — failing memory
// flush should never block summarization itself.
type SummarizationMemoryFlushHook func(ctx context.Context, before, after adk.ChatModelAgentState) error

// NewSummarization wraps eino/adk/middlewares/summarization.New with our
// own defaults: trigger at 190k tokens or 200 messages, no transcript
// file (we inject one once the sandbox layer can guarantee a writable
// path).
//
// The host calls this with primitive parameters so middlewares stays a
// leaf package and never imports agent (which would create a cycle).
// memoryFlush is optional; when nil, no Callback is registered and the
// middleware behaves as before.
//
// Returns (nil, nil) when enabled is false — the caller should simply
// skip appending the result to the chain in that case.
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
