// Package middleware defines the extension points used by the DeepAgent graph.
package middleware

import (
	"context"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// GraphInterruptHandle requests an interrupt on the currently running graph.
// It is kept here so middleware can expose approval-oriented tools without
// depending on the DeepAgent implementation.
type GraphInterruptHandle func(...compose.GraphInterruptOption)

// Middleware is the lifecycle contract for one graph middleware.
// BaseMiddleware supplies no-op implementations for optional hooks.
type Middleware interface {
	Name() string
	BeforeAgent(context.Context) error
	BuildStateHandler() types.RunTimeStateful
	BuildPrompt(context.Context) ([]*schema.Message, error)
	ModifyModelRequest(context.Context, []*schema.Message, []*schema.Message, *types.GraphState) ([]*schema.Message, error)
	ModifyModelResponse(context.Context, *schema.Message, *types.GraphState) (*schema.Message, error)
	ModifyModelStreamResponse(context.Context, *schema.StreamReader[*schema.Message], *types.GraphState) (*schema.StreamReader[*schema.Message], error)
	Tools(context.Context) ([]tool.BaseTool, error)
	ToolCallMiddlewares() []compose.ToolMiddleware
}

// BaseMiddleware makes every hook optional. Concrete middleware only needs to
// override the callbacks relevant to its feature.
type BaseMiddleware struct{}

func (BaseMiddleware) Name() string                                           { return "" }
func (BaseMiddleware) BeforeAgent(context.Context) error                      { return nil }
func (BaseMiddleware) BuildStateHandler() types.RunTimeStateful               { return nil }
func (BaseMiddleware) BuildPrompt(context.Context) ([]*schema.Message, error) { return nil, nil }
func (BaseMiddleware) ModifyModelRequest(_ context.Context, _ []*schema.Message, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
	return messages, nil
}
func (BaseMiddleware) ModifyModelResponse(_ context.Context, message *schema.Message, _ *types.GraphState) (*schema.Message, error) {
	return message, nil
}
func (BaseMiddleware) ModifyModelStreamResponse(_ context.Context, stream *schema.StreamReader[*schema.Message], _ *types.GraphState) (*schema.StreamReader[*schema.Message], error) {
	return stream, nil
}
func (BaseMiddleware) Tools(context.Context) ([]tool.BaseTool, error) { return nil, nil }
func (BaseMiddleware) ToolCallMiddlewares() []compose.ToolMiddleware  { return nil }

// MiddlewareChain applies middleware in declaration order.
type MiddlewareChain struct{ middlewares []Middleware }

func NewMiddlewareChain(middlewares ...Middleware) *MiddlewareChain {
	filtered := make([]Middleware, 0, len(middlewares))
	for _, middleware := range middlewares {
		if middleware != nil {
			filtered = append(filtered, middleware)
		}
	}
	return &MiddlewareChain{middlewares: filtered}
}

func (c *MiddlewareChain) BeforeAgent(ctx context.Context) error {
	if c == nil {
		return nil
	}
	for _, middleware := range c.middlewares {
		if err := middleware.BeforeAgent(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (c *MiddlewareChain) BuildPrompts(ctx context.Context) ([]*schema.Message, error) {
	if c == nil {
		return nil, nil
	}
	var prompts []*schema.Message
	for _, middleware := range c.middlewares {
		items, err := middleware.BuildPrompt(ctx)
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, items...)
	}
	return prompts, nil
}

func (c *MiddlewareChain) ModifyModelRequest(ctx context.Context, initial, messages []*schema.Message, state *types.GraphState) ([]*schema.Message, error) {
	if c == nil {
		return messages, nil
	}
	current := messages
	for _, middleware := range c.middlewares {
		var err error
		current, err = middleware.ModifyModelRequest(ctx, initial, current, state)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

func (c *MiddlewareChain) ModifyModelResponse(ctx context.Context, message *schema.Message, state *types.GraphState) (*schema.Message, error) {
	if c == nil {
		return message, nil
	}
	current := message
	for _, middleware := range c.middlewares {
		var err error
		current, err = middleware.ModifyModelResponse(ctx, current, state)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

func (c *MiddlewareChain) ModifyModelStreamResponse(ctx context.Context, stream *schema.StreamReader[*schema.Message], state *types.GraphState) (*schema.StreamReader[*schema.Message], error) {
	if c == nil {
		return stream, nil
	}
	current := stream
	for _, middleware := range c.middlewares {
		var err error
		current, err = middleware.ModifyModelStreamResponse(ctx, current, state)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

func (c *MiddlewareChain) BuildStateHandlers() map[string]types.RunTimeStateful {
	result := make(map[string]types.RunTimeStateful)
	if c == nil {
		return result
	}
	for _, middleware := range c.middlewares {
		if stateful := middleware.BuildStateHandler(); stateful != nil && middleware.Name() != "" {
			result[middleware.Name()] = stateful
		}
	}
	return result
}

func (c *MiddlewareChain) Tools(ctx context.Context) ([]tool.BaseTool, error) {
	if c == nil {
		return nil, nil
	}
	var result []tool.BaseTool
	for _, middleware := range c.middlewares {
		items, err := middleware.Tools(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
}

func (c *MiddlewareChain) ToolCallMiddlewares() []compose.ToolMiddleware {
	if c == nil {
		return nil
	}
	var result []compose.ToolMiddleware
	for _, middleware := range c.middlewares {
		result = append(result, middleware.ToolCallMiddlewares()...)
	}
	return result
}
