package middlewares

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

type ToolCallObservability struct {
	*adk.BaseChatModelAgentMiddleware
}

func NewToolCallObservability() *ToolCallObservability {
	return &ToolCallObservability{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	}
}

func (o *ToolCallObservability) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		name := ""
		if tCtx != nil {
			name = tCtx.Name
		}
		start := time.Now()
		out, err := endpoint(ctx, args, opts...)
		dur := time.Since(start)
		if err != nil {
			slog.Debug("tool.error",
				"name", name,
				"dur", dur,
				"in_size", len(args),
				"err", err,
			)
		} else {
			slog.Debug("tool.exit",
				"name", name,
				"dur", dur,
				"in_size", len(args),
				"out_size", len(out),
			)
		}
		return out, err
	}, nil
}
