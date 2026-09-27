package tools

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

type Mask func(context.Context, *schema.ToolInfo) bool

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

type NeedReviewAndEdit struct{ Message string }
type ApprovalGate func(context.Context, *ApprovalInfo) bool

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
	schema.RegisterName[*FollowUpInfo]("deepagent_follow_up_info")
	schema.RegisterName[*ReviewEditInfo]("deepagent_review_edit_info")
}
