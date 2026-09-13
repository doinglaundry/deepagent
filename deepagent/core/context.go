package deepagents

import (
	"context"
	"eino-cli/deepagent/core/types"
)

// GetDeepAgent 从上下文中获取当前 DeepAgent。
func GetDeepAgent(ctx context.Context) *DeepAgent {
	ins := ctx.Value("deep_agent")
	if ins == nil {
		return nil
	}
	agent, ok := ins.(*DeepAgent)
	if !ok {
		return nil
	}
	return agent
}

// GetWholeGraphState 获取当前 Agent 的完整图状态。
func GetWholeGraphState(ctx context.Context) *types.GraphState {
	a := GetDeepAgent(ctx)
	if a == nil {
		return nil
	}
	return a.GraphState()
}

// GetCustomGraphState 获取自定义的运行时状态。
func GetCustomGraphState(ctx context.Context, name string) types.RunTimeStateful {
	graphState := GetWholeGraphState(ctx)
	if graphState == nil {
		return nil
	}
	return graphState.GetStateful(name)
}
