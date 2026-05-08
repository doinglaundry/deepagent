package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
)

// DeferredTools mirrors deerflow.agents.middlewares.deferred_tools_middleware.
// The Python middleware intercepts a `register_tool` (or similar) call and
// late-binds the requested tool into the agent's tool list for subsequent
// turns.
//
// Phase 3 ships the BeforeAgent hook surface and a NameProvider abstraction;
// the actual deferred-registry plumbing lands when DeferredRegistry has a
// real backing source (currently agent.DeferredToolNamesFromConfig returns
// nil in the runtime/eino wiring when no deferred tools are configured).
type DeferredTools struct {
	*adk.BaseChatModelAgentMiddleware

	// NameProvider returns the names of deferred tools that should be
	// excluded from the active tool list. Wire this to the same source that
	// agent.DeferredToolNames pulls from so the prompt section and
	// the runtime tool list stay in sync.
	NameProvider func() []string

	Logger *slog.Logger
}

// NewDeferredTools returns a DeferredTools middleware. Only attach when
// AppConfig.ToolSearch.Enabled is true.
func NewDeferredTools(provider func() []string) *DeferredTools {
	return &DeferredTools{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		NameProvider:                 provider,
		Logger:                       slog.Default(),
	}
}

func (m *DeferredTools) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil || m.NameProvider == nil {
		return ctx, runCtx, nil
	}
	deferred := m.NameProvider()
	if len(deferred) == 0 {
		return ctx, runCtx, nil
	}
	deferredSet := make(map[string]struct{}, len(deferred))
	for _, n := range deferred {
		deferredSet[n] = struct{}{}
	}

	filtered := runCtx.Tools[:0]
	dropped := 0
	for _, t := range runCtx.Tools {
		info, err := t.Info(ctx)
		if err != nil || info == nil {
			filtered = append(filtered, t)
			continue
		}
		if _, isDeferred := deferredSet[info.Name]; isDeferred {
			dropped++
			continue
		}
		filtered = append(filtered, t)
	}
	runCtx.Tools = filtered
	if dropped > 0 {
		m.Logger.Debug("deferred-tools: filtered tools out of active set",
			"dropped", dropped, "total_after", len(runCtx.Tools))
	}
	return ctx, runCtx, nil
}
