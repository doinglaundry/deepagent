package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
)

// NewResearchTool retains the historical internal_subagent schema while using
// the same task execution and stream ownership as the task tool.
func NewResearchTool(runner ChildRunner) tool.BaseTool {
	return &researchTool{task: NewStreamingTaskTool(runner).(tool.StreamableTool)}
}

type researchTool struct{ task tool.StreamableTool }

func (*researchTool) ReadOnly() bool { return true }
func (*researchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "internal_subagent", Desc: "Delegate a bounded read-only research question to an independent in-process agent. Use task tools for distributed persistent work.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"task": {Type: schema.String, Required: true}})}, nil
}

func (s *researchTool) StreamableRun(ctx context.Context, arg string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	var input struct{ Task string }
	if err := json.Unmarshal([]byte(arg), &input); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Task) == "" {
		return nil, errors.New("task required")
	}
	raw, err := json.Marshal(map[string]string{"description": input.Task, "subagent_type": "research"})
	if err != nil {
		return nil, err
	}
	return s.task.StreamableRun(ctx, string(raw), opts...)
}
