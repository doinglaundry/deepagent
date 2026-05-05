// Package middlewares contains ChatModelAgent middlewares ported from
// deerflow.agents.middlewares. Each middleware embeds
// *adk.BaseChatModelAgentMiddleware so it only overrides the hooks it cares
// about.
package middlewares

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// ToolErrorHandling mirrors
// deerflow.agents.middlewares.tool_error_handling_middleware. It catches
// errors thrown by tool execution and converts them into a readable string
// returned to the model — preserving the agent loop instead of bubbling the
// error up and aborting the run. This is the Python equivalent of swallowing
// exceptions in wrap_invokable_tool_call and emitting a ToolMessage with the
// error text.
type ToolErrorHandling struct {
	*adk.BaseChatModelAgentMiddleware
	Logger *slog.Logger
}

// NewToolErrorHandling returns a ToolErrorHandling middleware ready for use
// in deep.Config.Handlers.
func NewToolErrorHandling() *ToolErrorHandling {
	return &ToolErrorHandling{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Logger:                       slog.Default(),
	}
}

func (m *ToolErrorHandling) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	name := ""
	if tCtx != nil {
		name = tCtx.Name
	}
	wrapped := func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		out, err := endpoint(ctx, args, opts...)
		if err != nil {
			m.Logger.Warn("tool call failed; converting to ToolMessage",
				"tool", name, "err", err)
			return fmt.Sprintf("Error executing tool %q: %s", name, err.Error()), nil
		}
		return out, nil
	}
	return wrapped, nil
}
