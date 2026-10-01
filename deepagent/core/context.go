package deepagents

import (
	"context"
	"eino-cli/deepagent/core/types"
)

// GetRun 从上下文中获取当前 Run。
func GetRun(ctx context.Context) *Run {
	ins := ctx.Value("run")
	if ins == nil {
		return nil
	}
	agent, ok := ins.(*Run)
	if !ok {
		return nil
	}
	return agent
}

// GetWholeGraphState 获取当前 Agent 的完整图状态。
func GetWholeGraphState(ctx context.Context) *types.GraphState {
	a := GetRun(ctx)
	if a == nil {
		return nil
	}
	return a.GraphState()
}
