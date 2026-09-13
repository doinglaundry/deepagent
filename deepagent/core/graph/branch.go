package graph

import (
	"context"
	"eino-cli/deepagent/core/constant"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// CreateBranchNode 创建 Model 节点后的默认路由分支。
//
// 路由规则：
//   - 若 LLM 调用了工具 → NodeKeyTools（服务端执行）
//   - 若 LLM 没有调用任何工具 → END
func CreateBranchNode(enableStream bool) *compose.GraphBranch {
	endNodes := map[string]bool{constant.NodeKeyTools: true, compose.END: true}
	if !enableStream {
		return compose.NewGraphBranch(func(_ context.Context, message *schema.Message) (string, error) {
			if message != nil && len(message.ToolCalls) > 0 {
				return constant.NodeKeyTools, nil
			}
			return compose.END, nil
		}, endNodes)
	}
	return compose.NewStreamGraphBranch(func(ctx context.Context, input *schema.StreamReader[*schema.Message]) (string, error) {
		hasToolCall, err := StreamHasToolCall(ctx, input)
		if err != nil {
			return "", err
		}
		if hasToolCall {
			return constant.NodeKeyTools, nil
		}
		return compose.END, nil
	}, endNodes)
}
