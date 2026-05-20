package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
)

// DeferredTools mirrors deerflow.agents.middlewares.deferred_tools_middleware.
// It excludes the configured names from the active tool list before each run.
type DeferredTools struct {
	*adk.BaseChatModelAgentMiddleware

	Names []string

	Logger *slog.Logger
}

// NewDeferredTools returns a DeferredTools middleware for code-defined deferred tools.
func NewDeferredTools(names []string) *DeferredTools {
	return &DeferredTools{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Names:                        names,
		Logger:                       slog.Default(),
	}
}

func (m *DeferredTools) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil || len(m.Names) == 0 {
		return ctx, runCtx, nil
	}
	deferredSet := make(map[string]struct{}, len(m.Names))
	for _, n := range m.Names {
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
