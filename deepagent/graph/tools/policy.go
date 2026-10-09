package tools

import (
	"context"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/schema"
)

func CombineMasks(firstMask, secondMask agentmodel.Mask) agentmodel.Mask {
	if firstMask == nil {
		return secondMask
	}
	if secondMask == nil {
		return firstMask
	}
	return func(ctx context.Context, toolInfo *schema.ToolInfo) bool {
		return firstMask(ctx, toolInfo) && secondMask(ctx, toolInfo)
	}
}
