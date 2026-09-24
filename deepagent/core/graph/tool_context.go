package graph

import (
	"context"
	"github.com/cloudwego/eino/compose"
)

// GetToolCallID returns the identity assigned by the current tool execution.
func GetToolCallID(ctx context.Context) string {
	if id, ok := ctx.Value(toolCallIDKey{}).(string); ok && id != "" {
		return id
	}
	return compose.GetToolCallID(ctx)
}
