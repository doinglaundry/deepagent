package tools

import (
	"context"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/schema"
)

type Mask func(context.Context, *schema.ToolInfo) bool

type ApprovalInfo struct {
	CallID    string
	ToolName  string
	Arguments string
	Reason    string
}

type ApprovalResult struct {
	CallID           string
	Approved         bool
	CancelRun        bool
	DisapproveReason *string
}

func CombineMasks(a, b Mask) Mask {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return func(ctx context.Context, info *schema.ToolInfo) bool { return a(ctx, info) && b(ctx, info) }
}

func init() {
	schema.RegisterName[*ApprovalInfo]("deepagent_approval_info")
}

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
