package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ChildRequest struct {
	Name          string
	Prompt        string
	MaxModelCalls int
}
type ChildRunner interface {
	Run(context.Context, ChildRequest, types.ModelChunkSink) (*schema.Message, error)
}

type taskTool struct {
	runner ChildRunner
	names  []string
}

func NewTaskTool(runner ChildRunner, names ...string) einotool.BaseTool {
	return &taskTool{runner: runner, names: append([]string(nil), names...)}
}
func (t *taskTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "task", Desc: "Run an independent subagent on a self-contained task and return its result. Omit subagent_type for general-purpose.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"subagent_type": {Type: schema.String, Enum: append([]string(nil), t.names...)}, "description": {Type: schema.String, Required: true}})}, nil
}
func (t *taskTool) InvokableRun(ctx context.Context, raw string, _ ...einotool.Option) (string, error) {
	if t.runner == nil {
		return "", fmt.Errorf("child runner is required")
	}
	request, err := parseChildRequest(raw)
	if err != nil {
		return "", err
	}
	message, err := t.runner.Run(ctx, request, nil)
	if err != nil {
		return "", err
	}
	if message == nil {
		return "", fmt.Errorf("child returned no message")
	}
	return message.Content, nil
}

func parseChildRequest(raw string) (ChildRequest, error) {
	var input struct {
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		Task         string `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return ChildRequest{}, err
	}
	prompt := input.Description
	if prompt == "" {
		prompt = input.Prompt
	}
	if prompt == "" {
		prompt = input.Task
	}
	if strings.TrimSpace(prompt) == "" {
		return ChildRequest{}, fmt.Errorf("description is required")
	}
	name := input.SubagentType
	if name == "" {
		name = "general-purpose"
	}
	return ChildRequest{Name: name, Prompt: prompt}, nil
}
