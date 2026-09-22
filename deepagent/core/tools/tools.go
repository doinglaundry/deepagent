package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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

func NewInvokablePolicyTool(inner tool.InvokableTool, gate ToolPolicyGate) tool.BaseTool {
	return &policyTool{InvokableTool: inner, gate: gate}
}
func NewInvokableReviewEditTool(inner tool.InvokableTool, _ NeedReviewAndEdit) tool.BaseTool {
	return inner
}
func GetFollowUpTool() tool.BaseTool { return &followUpTool{} }

type ApprovalResult struct {
	Approved         bool
	DisapproveReason *string
}
type FollowUpResult struct{ UserAnswer string }

func init() {
	schema.RegisterName[*ApprovalInfo]("deepagent_approval_info")
	schema.RegisterName[*FollowUpInfo]("deepagent_follow_up_info")
	schema.RegisterName[*ReviewEditInfo]("deepagent_review_edit_info")
}

type followUpTool struct{}

func (*followUpTool) ReadOnly() bool { return true }

func (*followUpTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "ask_user",
		Desc: "Pause and ask the user one question. Execution resumes with the user's answer.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"question": {Type: schema.String, Required: true},
			"options":  {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
		}),
	}, nil
}

func (*followUpTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	if target, hasData, resumed := tool.GetResumeContext[*FollowUpInfo](ctx); target {
		if !hasData || resumed == nil || strings.TrimSpace(resumed.UserAnswer) == "" {
			return "", errors.New("follow-up resume requires an answer")
		}
		return resumed.UserAnswer, nil
	}
	var input struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "", err
	}
	input.Question = strings.TrimSpace(input.Question)
	if input.Question == "" {
		return "", errors.New("question is required")
	}
	return "", tool.Interrupt(ctx, &FollowUpInfo{Question: input.Question, Questions: append([]string(nil), input.Options...)})
}

type policyTool struct {
	tool.InvokableTool
	gate ToolPolicyGate
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
