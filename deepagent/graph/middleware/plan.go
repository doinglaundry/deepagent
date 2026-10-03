package middleware

import (
	"context"
	"fmt"
	"strings"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/schema"
)

type Plan struct{ BaseMiddleware }

func NewPlan() Middleware { return &Plan{} }

func (*Plan) GetName() string { return "plan" }

const reminderTag = `<system_reminder type="plan">`

// The plan belongs to RunState. This middleware retains no copy, so compaction
// and checkpoint restore cannot leave a stale middleware-owned plan behind.
func (*Plan) ModifyModelRequest(ctx context.Context, _ []*schema.Message, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
	runState := types.GetRunState(ctx)
	if runState == nil || len(runState.Plan) == 0 {
		return messages, nil
	}
	for _, message := range messages {
		if message == nil {
			continue
		}
		if message.Role == schema.System && strings.Contains(message.Content, reminderTag) {
			return messages, nil
		}
		if message.Role != schema.Assistant {
			continue
		}
		for _, toolCall := range message.ToolCalls {
			if toolCall.Function.Name == "update_plan" || toolCall.Function.Name == "write_todos" {
				return messages, nil
			}
		}
	}
	var reminderPrompt strings.Builder
	reminderPrompt.WriteString(reminderTag + "\nCurrent plan from earlier context:\n")
	for _, planStep := range runState.Plan {
		fmt.Fprintf(&reminderPrompt, "- [%s] %s\n", planStep.Status, planStep.Step)
	}
	reminderPrompt.WriteString("Update this plan with update_plan as work progresses.\n</system_reminder>")
	return append([]*schema.Message{schema.SystemMessage(reminderPrompt.String())}, messages...), nil
}

// Planning guidance is request context; the plan itself remains in RunState.
func (*Plan) BuildPrompt(context.Context) ([]*schema.Message, error) {
	return []*schema.Message{schema.SystemMessage(`<plan_mode>
Use update_plan to maintain the task plan for work with multiple steps.
Mark a step in_progress before starting and completed when finished.
Keep at most one step in_progress unless work actually runs in parallel.
Revise the plan as new work is discovered and remove irrelevant steps.
Skip plan updates for trivial requests.
</plan_mode>`)}, nil
}
