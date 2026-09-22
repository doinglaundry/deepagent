package plan

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"eino-cli/deepagent/core/middlewares"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type Middleware struct {
	middleware.BaseMiddleware
	onUpdate PlanUpdateHandler
}

type PlanMiddlewareConfig struct{ OnPlanUpdate PlanUpdateHandler }

type PlanStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

type PlanUpdate struct {
	Plan        []PlanStep `json:"plan"`
	Explanation string     `json:"explanation,omitempty"`
}

type PlanUpdateHandler func(context.Context, PlanUpdate) error

func New(cfg *PlanMiddlewareConfig) middleware.Middleware {
	if cfg == nil {
		cfg = &PlanMiddlewareConfig{}
	}
	return &Middleware{onUpdate: cfg.OnPlanUpdate}
}

func (m *Middleware) Name() string { return "plan" }

func (m *Middleware) Tools(context.Context) ([]tool.BaseTool, error) {
	return []tool.BaseTool{&updatePlanTool{onUpdate: m.onUpdate}}, nil
}

type updatePlanTool struct{ onUpdate PlanUpdateHandler }

func (*updatePlanTool) ReadOnly() bool { return true }

func (*updatePlanTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "update_plan",
		Desc: "Publish the current plan and progress.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"explanation": {Type: schema.String},
			"plan": {Type: schema.Array, Required: true, ElemInfo: &schema.ParameterInfo{
				Type: schema.Object,
				SubParams: map[string]*schema.ParameterInfo{
					"step":   {Type: schema.String, Required: true},
					"status": {Type: schema.String, Required: true, Enum: []string{"pending", "in_progress", "completed"}},
				},
			}},
		}),
	}, nil
}

func (t *updatePlanTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var update PlanUpdate
	if err := json.Unmarshal([]byte(arguments), &update); err != nil {
		return "", err
	}
	if len(update.Plan) == 0 {
		return "", errors.New("plan is required")
	}
	for _, step := range update.Plan {
		if strings.TrimSpace(step.Step) == "" || !validStatus(step.Status) {
			return "", errors.New("invalid plan step")
		}
	}
	if t.onUpdate != nil {
		if err := t.onUpdate(ctx, update); err != nil {
			return "", err
		}
	}
	return arguments, nil
}

func validStatus(status string) bool {
	return status == "pending" || status == "in_progress" || status == "completed"
}
