package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// MemoryHooks is the host-supplied surface for memory injection /
// extraction. These mirror the Python protocol used by
// deerflow.agents.middlewares.memory_middleware.
type MemoryHooks struct {
	// Inject is called on the first model turn per Run with the existing
	// message history. Implementations should prepend a system / user
	// "<memory>...</memory>" block (or no-op when there's nothing to inject)
	// and return the rewritten slice.
	Inject func(ctx context.Context, messages []*schema.Message) []*schema.Message

	// Extract is called after each model turn so the host can mine new
	// long-term facts asynchronously. Returning quickly is recommended.
	Extract func(ctx context.Context, messages []*schema.Message)
}

// Memory mirrors deerflow.agents.middlewares.memory_middleware. Phase 3
// ships the hook surface — wire MemoryHooks.Inject / Extract from the
// runtime layer once a real memory store is plugged in. Without hooks
// attached the middleware is a no-op.
type Memory struct {
	*adk.BaseChatModelAgentMiddleware

	Hooks  MemoryHooks
	Logger *slog.Logger

	injected bool
}

// NewMemory returns a Memory middleware. Attach when AppConfig.Memory.Enabled
// is true; pass MemoryHooks for the host-side data plane.
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
	mc *adk.ModelContext,
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
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || m.Hooks.Extract == nil {
		return ctx, state, nil
	}
	go m.Hooks.Extract(ctx, append([]*schema.Message(nil), state.Messages...))
	return ctx, state, nil
}
