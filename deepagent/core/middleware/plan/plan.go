package plan

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
)

type Middleware struct{ middleware.BaseMiddleware }
type PlanMiddlewareConfig struct {
	ToolMask     tools.Mask
	OnPlanUpdate PlanUpdateHandler
}
type PlanStep struct {
	Step   string
	Status string
}
type PlanUpdate struct {
	Plan        []PlanStep
	Explanation string
}
type PlanUpdateHandler func(context.Context, PlanUpdate) error

func New(_ *PlanMiddlewareConfig) middleware.Middleware              { return &Middleware{} }
func (m *Middleware) Name() string                                   { return "plan" }
func (m *Middleware) Tools(context.Context) ([]tool.BaseTool, error) { return nil, nil }
