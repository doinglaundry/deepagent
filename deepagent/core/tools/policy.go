package tools

import (
	"context"

	"eino-cli/deepagent/core/types"
)

// Policy decides before a tool can produce a side effect.
type Policy interface {
	Decide(context.Context, types.ToolCall, ToolDescriptor) (Decision, error)
}

type PolicyFunc func(context.Context, types.ToolCall, ToolDescriptor) (Decision, error)

func (f PolicyFunc) Decide(ctx context.Context, call types.ToolCall, descriptor ToolDescriptor) (Decision, error) {
	return f(ctx, call, descriptor)
}

type Action string

const (
	Allow       Action = "allow"
	Deny        Action = "deny"
	AskApproval Action = "ask_approval"
)

type Decision struct {
	Action Action
	Reason string
}
