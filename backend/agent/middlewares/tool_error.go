// Package middlewares contains ChatModelAgent middlewares for the lead agent.
package middlewares

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// ToolErrorHandling converts tool-call errors into a string ToolMessage so the
// agent loop continues instead of aborting the run.
type ToolErrorHandling struct {
	*adk.BaseChatModelAgentMiddleware
	Logger *slog.Logger
}

// NewToolErrorHandling returns a ToolErrorHandling middleware.
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
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		out, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			m.Logger.Warn("tool call failed; converting to ToolMessage",
				"tool", name, "err", err)
			return fmt.Sprintf("Error executing tool %q: %s", name, err.Error()), nil
		}
		return out, nil
	}, nil
}
