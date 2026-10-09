// Package middleware defines the extension points used by the Run graph.
package middleware

import (
	"context"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/schema"
)

// BaseMiddleware makes every hook optional. Concrete middleware only needs to
// override the callbacks relevant to its feature.
type BaseMiddleware struct{}

func (BaseMiddleware) GetName() string { return "" }

func (BaseMiddleware) PrepareAgent(context.Context) error { return nil }

func (BaseMiddleware) GetStateHandler() agentmodel.RunTimeStateful { return nil }

func (BaseMiddleware) BuildPrompt(context.Context) ([]*agentmodel.Message, error) { return nil, nil }

func (BaseMiddleware) ModifyModelRequest(_ context.Context, _ []*agentmodel.Message, messages []*agentmodel.Message, _ *agentmodel.GraphState) ([]*agentmodel.Message, error) {
	return messages, nil
}

func (BaseMiddleware) ModifyModelResponse(_ context.Context, message *agentmodel.Message, _ *agentmodel.GraphState) (*agentmodel.Message, error) {
	return message, nil
}

func (BaseMiddleware) ModifyModelStreamResponse(_ context.Context, stream *schema.StreamReader[*agentmodel.Message], _ *agentmodel.GraphState) (*schema.StreamReader[*agentmodel.Message], error) {
	return stream, nil
}
