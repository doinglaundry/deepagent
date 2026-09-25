package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type PlanStep = types.PlanStep

const ToolUpdatePlan = "update_plan"

type PlanUpdate struct {
	Plan        []PlanStep `json:"plan"`
	Explanation string     `json:"explanation,omitempty"`
}
type PlanUpdateHandler func(context.Context, PlanUpdate) error

type updatePlanTool struct{ onUpdate PlanUpdateHandler }

func NewUpdatePlanTool(onUpdate PlanUpdateHandler) tool.InvokableTool {
	return &updatePlanTool{onUpdate: onUpdate}
}
func (*updatePlanTool) ReadOnly() bool { return true }
func (*updatePlanTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: ToolUpdatePlan, Desc: "Publish the current plan and progress.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"explanation": {Type: schema.String},
		"plan": {Type: schema.Array, Required: true, ElemInfo: &schema.ParameterInfo{Type: schema.Object, SubParams: map[string]*schema.ParameterInfo{
			"step":   {Type: schema.String, Required: true},
			"status": {Type: schema.String, Required: true, Enum: []string{"pending", "in_progress", "completed"}},
		}}},
	})}, nil
}
func (t *updatePlanTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	update, err := normalizePlanArgs(raw)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t.onUpdate != nil {
		if err := t.onUpdate(ctx, update); err != nil {
			return "", err
		}
	}
	if state := types.RunStateFromContext(ctx); state != nil {
		state.Plan = append([]types.PlanStep(nil), update.Plan...)
	}
	if err := types.EmitEvent(ctx, types.RuntimeEvent{Kind: "plan_updated", Data: update}); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(update)
	return string(encoded), err
}

func normalizePlanArgs(raw string) (PlanUpdate, error) {
	var object struct {
		Plan  json.RawMessage `json:"plan"`
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
		Explanation string `json:"explanation"`
	}
	var update PlanUpdate
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return update, errors.New("plan is required")
	}
	if raw[0] != '{' {
		text := raw
		if raw[0] == '"' {
			if err := json.Unmarshal([]byte(raw), &text); err != nil {
				return update, err
			}
		}
		if raw[0] == '[' {
			return update, errors.New("plan must be an object or plain text")
		}
		update.Plan = []PlanStep{{Step: text, Status: "in_progress"}}
	} else {
		if err := json.Unmarshal([]byte(raw), &object); err != nil {
			return update, err
		}
		update.Explanation = object.Explanation
		// Legacy todos takes precedence over a legacy plain-text plan.
		if len(object.Todos) > 0 {
			for _, todo := range object.Todos {
				update.Plan = append(update.Plan, PlanStep{Step: todo.Content, Status: todo.Status})
			}
		} else if len(object.Plan) > 0 && object.Plan[0] == '"' {
			var text string
			if err := json.Unmarshal(object.Plan, &text); err != nil {
				return update, err
			}
			update.Plan = []PlanStep{{Step: text, Status: "in_progress"}}
		} else if err := json.Unmarshal(object.Plan, &update.Plan); err != nil {
			return update, err
		}
	}
	if len(update.Plan) == 0 {
		return update, errors.New("plan is required")
	}
	for _, step := range update.Plan {
		if strings.TrimSpace(step.Step) == "" || (step.Status != "pending" && step.Status != "in_progress" && step.Status != "completed") {
			return update, errors.New("invalid plan step")
		}
	}
	return update, nil
}
