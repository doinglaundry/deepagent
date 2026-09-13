package tools

import (
	"context"
	"errors"

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
	ToolName  string
	Arguments string
	Reason    string
}
type FollowUpInfo struct{ Question string }
type ReviewEditInfo struct {
	ToolName  string
	Arguments string
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

type invokableWrapper struct {
	tool.InvokableTool
	info   *schema.ToolInfo
	invoke func(context.Context, string, ...tool.Option) (string, error)
}

func (w *invokableWrapper) Info(context.Context) (*schema.ToolInfo, error) { return w.info, nil }
func (w *invokableWrapper) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	if w.invoke == nil {
		return "", errors.New("tool invocation is unavailable")
	}
	return w.invoke(ctx, args, opts...)
}
func NewInvokablePolicyTool(inner tool.InvokableTool, gate ToolPolicyGate) tool.BaseTool {
	return inner
}
func NewInvokableReviewEditTool(inner tool.InvokableTool, _ NeedReviewAndEdit) tool.BaseTool {
	return inner
}
func GetFollowUpTool() tool.BaseTool                                   { return nil }
func WithWrapperCallbacksDisabled(ctx context.Context) context.Context { return ctx }
