package deepagents

import (
	"context"
	"eino-cli/deepagent/core/graph"
)

type ToolNodePreHandler = graph.ToolNodePreHandler
type ToolNodePostHandler = graph.ToolNodePostHandler

func New(ctx context.Context, opts ...Option) (*DeepAgent, error) { return graph.New(ctx, opts...) }
