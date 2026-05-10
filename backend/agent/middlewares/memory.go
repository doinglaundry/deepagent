package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// MemoryHooks is the host surface for memory injection / extraction.
// Inject runs once on the first model turn per Run; Extract runs
// asynchronously after each turn (return quickly).
type MemoryHooks struct {
	Inject  func(ctx context.Context, messages []*schema.Message) []*schema.Message
	Extract func(ctx context.Context, messages []*schema.Message)
}

// Memory injects a <memory> block on the first turn and fires Extract after each turn.
type Memory struct {
	*adk.BaseChatModelAgentMiddleware

	Hooks  MemoryHooks
	Logger *slog.Logger

	injected bool
}

// NewMemory returns a Memory middleware; attach when AppConfig.Memory.Enabled.
func NewMemory(hooks MemoryHooks) *Memory {
	return &Memory{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Hooks:                        hooks,
		Logger:                       slog.Default(),
	}
}

func (m *Memory) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || m.injected || m.Hooks.Inject == nil {
		return ctx, state, nil
	}
	state.Messages = m.Hooks.Inject(ctx, state.Messages)
	m.injected = true
	return ctx, state, nil
}

func (m *Memory) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || m.Hooks.Extract == nil {
		return ctx, state, nil
	}
	go m.Hooks.Extract(ctx, append([]*schema.Message(nil), state.Messages...))
	return ctx, state, nil
}
