package graph

import (
	"context"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type SubAgent struct {
	Name, SystemPrompt                    string
	MaxSteps                              int
	EnableFilesystem, EnableWeb, ReadOnly bool
	ToolMask                              tools.Mask
	Tools                                 []tool.BaseTool
}
type SubAgentContextInjector func(context.Context, string) ([]*schema.Message, error)
