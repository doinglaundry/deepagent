package tools

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type Mask func(context.Context, *schema.ToolInfo) bool
type ToolInfoRewriter func(context.Context, *schema.ToolInfo) (*schema.ToolInfo, error)
type ToolCallDecision struct {
	Action string
	Reason string
}

const ToolCallDeny = "deny"

type ApprovalInfo struct {
	CallID          string
	ArgumentsInJSON string
	ToolName        string
	Arguments       string
	Reason          string
}
type FollowUpInfo struct {
	Question, UserAnswer string
	Questions            []string
}
type ReviewEditInfo struct {
	ArgumentsInJSON string
	ToolName        string
	Arguments       string
}
type ToolPolicyGate struct {
	Policy        func(context.Context, *ApprovalInfo) (ToolCallDecision, error)
	DenyFormatter func(context.Context, *ApprovalInfo, ToolCallDecision) (string, error)
}
type NeedReviewAndEdit struct{ Message string }
type ApprovalGate func(context.Context, *ApprovalInfo) bool

type WrapToolsConfig struct{ InfoRewriter ToolInfoRewriter }

func CombineMasks(a, b Mask) Mask {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return func(ctx context.Context, info *schema.ToolInfo) bool { return a(ctx, info) && b(ctx, info) }
}
func WrapToolsWithConfig(in []tool.BaseTool, cfg *WrapToolsConfig) []tool.BaseTool {
	if cfg == nil || cfg.InfoRewriter == nil {
		return in
	}
	out := make([]tool.BaseTool, 0, len(in))
	for _, t := range in {
		out = append(out, &rewrittenTool{inner: t, rewrite: cfg.InfoRewriter})
	}
	return out
}

type rewrittenTool struct {
	inner   tool.BaseTool
	rewrite ToolInfoRewriter
}

func (t *rewrittenTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	info, err := t.inner.Info(ctx)
	if err != nil || info == nil {
		return info, err
	}
	return t.rewrite(ctx, info)
}

func NewInvokableReviewEditTool(inner tool.InvokableTool, _ NeedReviewAndEdit) tool.BaseTool {
	return inner
}
func init() {
	schema.RegisterName[*ApprovalInfo]("deepagent_approval_info")
	schema.RegisterName[*FollowUpInfo]("deepagent_follow_up_info")
	schema.RegisterName[*ReviewEditInfo]("deepagent_review_edit_info")
}
