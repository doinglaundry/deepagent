package deepagents

import (
	"context"
	"eino-cli/deepagent/core/graph"
)

func New(ctx context.Context, opts ...Option) (*DeepAgent, error) { return graph.New(ctx, opts...) }
