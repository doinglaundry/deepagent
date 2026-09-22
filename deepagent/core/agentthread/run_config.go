package agentthread

import (
	"context"
	deepagents "eino-cli/deepagent/core"
	"eino-cli/deepagent/core/middlewares"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// RunConfig combines one DeepAgent configuration with hooks that only
// exist at the thread execution boundary.
type RunConfig struct {
	Agent deepagents.Config
	// EnablePlan enables the lightweight update_plan progress checklist.
	// It injects PlanMiddleware and emits EventPlanUpdated whenever the model
	// successfully updates the checklist.
	EnablePlan bool
	// EventIDProvider: 外部提供的事件ID生成器（可选）。
	// 形参：ctx、threadID、runID；返回下一个 EventID（string）。
	// 若为空，则直接使用 uuid.New().String() 作为 EventID。
	EventIDProvider func(ctx context.Context, threadID, runID string) string
	// MiddlewaresProvider: 每轮构造 DeepAgent 时调用的中间件提供者（动态）。
	// 形参：ctx、runID；返回需要注入的中间件切片。
	// Provider 返回的中间件排在 Agent.Middlewares 之前。
	MiddlewaresProvider func(ctx context.Context, runID string) []middleware.Middleware
	// CustomStateBuilder: 自定义状态构建器（可选）。
	// 形参：ctx、threadID、runID；返回自定义状态的 map[string]types.RunTimeStateful。
	// CustomStateBuilder 会在每次执行器构建时执行，将结果写入 graphState，随 graphState 一同保存和恢复。
	// key 是状态名，必须唯一，否则会覆盖。
	CustomStateBuilder func(ctx context.Context, threadID, runID string) map[string]types.RunTimeStateful
	// RunCompleted observes a successfully persisted logical run. Hosts use
	// this boundary for best-effort side effects such as memory extraction.
	RunCompleted func(ctx context.Context, threadID, runID string, chatModel model.ToolCallingChatModel, history []*schema.Message) `json:"TurnCompleted" yaml:"turncompleted"`
}

// Clone returns a run-local copy of the config.
// Dependency objects such as models, backends, stores, loaders, and middleware
// instances are intentionally shared by reference. Container fields are copied
// so changes made for one run cannot mutate the thread-level base config.
func (c *RunConfig) Clone() (cloned *RunConfig) {
	if c == nil {
		return &RunConfig{}
	}
	value := *c
	cloned = &value
	cloned.Agent = *c.Agent.Clone()
	return cloned
}
