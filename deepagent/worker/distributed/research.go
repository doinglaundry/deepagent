package distributed

import (
	"context"
	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/tools"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
)

func newResearchTool(ctx context.Context, selected model.ToolCallingChatModel, candidates []tool.BaseTool) (tool.BaseTool, error) {
	var available []tool.BaseTool
	for _, t := range candidates {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, errors.New("tool info required")
		}
		if info.Name == "internal_subagent" || info.Name == "ask_user" {
			continue
		}
		if req, ok := t.(interface{ RequiresApproval() bool }); ok && req.RequiresApproval() {
			continue
		}
		available = append(available, t)
	}
	runner := graph.NewChildRunner(graph.Config{Model: selected, Tools: available, ReadOnlyToolsOnly: true, SubAgents: []*graph.SubAgent{{Name: "research", ReadOnly: true, MaxSteps: 24, SystemPrompt: "Research the assigned question using read-only tools. Return factual findings, sources, and uncertainties. Do not attempt mutations or delegation."}}})
	return tools.NewResearchTool(runner), nil
}
