// Package middleware defines the extension points used by the Run graph.
package middleware

import (
	"context"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/schema"
)

// Middleware is the lifecycle contract for one graph middleware.
// BaseMiddleware supplies no-op implementations for optional hooks.
type Middleware interface {
	GetName() string
	PrepareAgent(context.Context) error
	GetStateHandler() types.RunTimeStateful
	BuildPrompt(context.Context) ([]*messagepkg.Message, error)
	ModifyModelRequest(context.Context, []*messagepkg.Message, []*messagepkg.Message, *types.GraphState) ([]*messagepkg.Message, error)
	ModifyModelResponse(context.Context, *messagepkg.Message, *types.GraphState) (*messagepkg.Message, error)
	ModifyModelStreamResponse(context.Context, *schema.StreamReader[*messagepkg.Message], *types.GraphState) (*schema.StreamReader[*messagepkg.Message], error)
}

// BaseMiddleware makes every hook optional. Concrete middleware only needs to
// override the callbacks relevant to its feature.
type BaseMiddleware struct{}

func (BaseMiddleware) GetName() string { return "" }

func (BaseMiddleware) PrepareAgent(context.Context) error { return nil }

func (BaseMiddleware) GetStateHandler() types.RunTimeStateful { return nil }

func (BaseMiddleware) BuildPrompt(context.Context) ([]*messagepkg.Message, error) { return nil, nil }

func (BaseMiddleware) ModifyModelRequest(_ context.Context, _ []*messagepkg.Message, messages []*messagepkg.Message, _ *types.GraphState) ([]*messagepkg.Message, error) {
	return messages, nil
}

func (BaseMiddleware) ModifyModelResponse(_ context.Context, message *messagepkg.Message, _ *types.GraphState) (*messagepkg.Message, error) {
	return message, nil
}

func (BaseMiddleware) ModifyModelStreamResponse(_ context.Context, stream *schema.StreamReader[*messagepkg.Message], _ *types.GraphState) (*schema.StreamReader[*messagepkg.Message], error) {
	return stream, nil
}

// Optional capabilities are detected directly by Graph; there is no second
// middleware pipeline or intermediate execution object.
type RunMiddleware interface {
	PrepareRun(context.Context, *types.RunState) error
	FinishRun(context.Context, *types.RunState, error) error
}

type EventObserver interface {
	Observe(context.Context, types.RuntimeEvent) error
}

type ModelHandler func(context.Context, []*messagepkg.Message) (*schema.StreamReader[*messagepkg.Message], error)

type ModelMiddleware interface {
	WrapModel(ModelHandler) ModelHandler
}

// RunFactory creates fresh mutable state when a configured middleware is reused.
type RunFactory interface{ NewRun() Middleware }

// ResourceCloser releases resources acquired during setup or execution. Close must tolerate construction without PrepareRun.
type ResourceCloser interface{ Close(context.Context) error }
