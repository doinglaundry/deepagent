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

func CombineMasks(firstMask, secondMask Mask) Mask {
	if firstMask == nil {
		return secondMask
	}
	if secondMask == nil {
		return firstMask
	}
	return func(ctx context.Context, toolInfo *schema.ToolInfo) bool {
		return firstMask(ctx, toolInfo) && secondMask(ctx, toolInfo)
	}
}

func init() {
	schema.RegisterName[*ApprovalInfo]("deepagent_approval_info")
}

// Policy decides before a tool can produce a side effect.
type Policy interface {
	Decide(context.Context, types.ToolCall, ToolDescriptor) (Decision, error)
}

type PolicyFunc func(context.Context, types.ToolCall, ToolDescriptor) (Decision, error)

func (policyFunc PolicyFunc) Decide(ctx context.Context, toolCall types.ToolCall, toolDescriptor ToolDescriptor) (Decision, error) {
	return policyFunc(ctx, toolCall, toolDescriptor)
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
