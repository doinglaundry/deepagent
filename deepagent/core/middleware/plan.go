package middleware

import (
	"context"
	"fmt"
	"strings"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type Plan struct {
	BaseMiddleware
	onUpdate PlanUpdateHandler
}
type PlanMiddlewareConfig struct{ OnPlanUpdate PlanUpdateHandler }
type PlanStep = tools.PlanStep
type PlanUpdate = tools.PlanUpdate
type PlanUpdateHandler = tools.PlanUpdateHandler

func NewPlan(cfg *PlanMiddlewareConfig) Middleware {
	if cfg == nil {
		cfg = &PlanMiddlewareConfig{}
	}
	return &Plan{onUpdate: cfg.OnPlanUpdate}
}
func (*Plan) Name() string { return "plan" }
func (m *Plan) Tools(context.Context) ([]tool.BaseTool, error) {
	return []tool.BaseTool{tools.NewUpdatePlanTool(m.onUpdate)}, nil
}

const reminderTag = `<system_reminder type="plan">`

// The plan belongs to RunState. This middleware retains no copy, so compaction
// and checkpoint restore cannot leave a stale middleware-owned plan behind.
func (*Plan) ModifyModelRequest(ctx context.Context, _ []*schema.Message, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
	state := types.RunStateFromContext(ctx)
	if state == nil || len(state.Plan) == 0 {
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
		for _, call := range message.ToolCalls {
			if call.Function.Name == "update_plan" || call.Function.Name == "write_todos" {
				return messages, nil
			}
		}
	}
	var prompt strings.Builder
	prompt.WriteString(reminderTag + "\nCurrent plan from earlier context:\n")
	for _, step := range state.Plan {
		fmt.Fprintf(&prompt, "- [%s] %s\n", step.Status, step.Step)
	}
	prompt.WriteString("Update this plan with update_plan as work progresses.\n</system_reminder>")
	return append([]*schema.Message{schema.SystemMessage(prompt.String())}, messages...), nil
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
