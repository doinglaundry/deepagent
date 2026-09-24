package tools

import (
	"context"
	"errors"

	"eino-cli/deepagent/core/types"

	"github.com/cloudwego/eino/components/tool"
)

type policyTool struct {
	tool.InvokableTool
	gate ToolPolicyGate
}

func NewInvokablePolicyTool(inner tool.InvokableTool, gate ToolPolicyGate) tool.BaseTool {
	return &policyTool{InvokableTool: inner, gate: gate}
}

func (t *policyTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	info, err := t.Info(ctx)
	if err != nil {
		return "", err
	}
	approval := &ApprovalInfo{ArgumentsInJSON: args, Arguments: args}
	if info != nil {
		approval.ToolName = info.Name
	}
	decision, err := t.gate.Policy(ctx, approval)
	if err != nil {
		return "", err
	}
	if decision.Action != ToolCallDeny {
		return t.InvokableTool.InvokableRun(ctx, args, opts...)
	}
	if t.gate.DenyFormatter != nil {
		return t.gate.DenyFormatter(ctx, approval, decision)
	}
	if decision.Reason == "" {
		decision.Reason = "tool call denied"
	}
	return "", errors.New(decision.Reason)
}

// Policy decides before a tool can produce a side effect.
type Policy interface {
	Decide(context.Context, types.ToolCall, Descriptor) (Decision, error)
}

type PolicyFunc func(context.Context, types.ToolCall, Descriptor) (Decision, error)

func (f PolicyFunc) Decide(ctx context.Context, call types.ToolCall, descriptor Descriptor) (Decision, error) {
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
