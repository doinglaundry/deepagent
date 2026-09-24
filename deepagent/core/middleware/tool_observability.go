package middleware

import (
	"context"
	"log/slog"
	"time"

	"eino-cli/deepagent/core/types"
)

// ToolCallObservability logs metadata around the existing tool execution path.
// It has no per-run state and never logs arguments or successful result contents.
type ToolCallObservability struct {
	BaseMiddleware
	Logger *slog.Logger
}

func NewToolCallObservability() *ToolCallObservability { return &ToolCallObservability{} }
func (*ToolCallObservability) Name() string            { return "tool_observability" }
func (o *ToolCallObservability) WrapTool(next ToolHandler) ToolHandler {
	return func(ctx context.Context, call types.ToolCall) (*types.ToolResult, error) {
		start := time.Now()
		result, err := next(ctx, call)
		logger := o.Logger
		if logger == nil {
			logger = slog.Default()
		}
		attrs := []any{"name", call.Name, "dur", time.Since(start), "in_size", len(call.Arguments)}
		if err != nil || (result != nil && result.IsError) {
			if err != nil {
				attrs = append(attrs, "err", err)
			}
			logger.DebugContext(ctx, "tool.error", attrs...)
		} else {
			size := 0
			if result != nil {
				size = len(result.Content)
			}
			logger.DebugContext(ctx, "tool.exit", append(attrs, "out_size", size)...)
		}
		return result, err
	}
}
