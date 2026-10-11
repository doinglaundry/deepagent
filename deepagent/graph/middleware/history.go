package middleware

import (
	"context"

	agentmodel "eino-cli/deepagent/model"
)

type patchToolCalls struct{ BaseMiddleware }

func NewPatchToolCalls() agentmodel.Middleware { return &patchToolCalls{} }

func (*patchToolCalls) GetName() string { return "patch_tool_calls" }

func (*patchToolCalls) ModifyModelRequest(_ context.Context, _ []*agentmodel.Message, messages []*agentmodel.Message, _ *agentmodel.GraphState) ([]*agentmodel.Message, error) {
	return agentmodel.PatchDanglingToolCalls(messages), nil
}
