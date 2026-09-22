// Package middlewares contains ChatModelAgent middlewares for the lead agent.
package middlewares

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

type ToolErrorHandling struct {
	*adk.BaseChatModelAgentMiddleware
	Logger *slog.Logger
}

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

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		name := ""
		if tCtx != nil {
			name = tCtx.Name
		}
		out, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return fmt.Sprintf("Error executing tool %q: %s", name, err.Error()), nil
		}
		return out, nil
	}, nil
}
