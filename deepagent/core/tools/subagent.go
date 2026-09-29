package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	description := "Run an independent subagent on a self-contained task and return its result. Omit subagent_type for general-purpose."
	subagentType := &schema.ParameterInfo{Type: schema.String, Enum: append([]string(nil), t.names...)}
	if len(t.names) > 0 && !slices.Contains(t.names, "general-purpose") {
		description = fmt.Sprintf("Run an independent subagent on a self-contained task and return its result. subagent_type is required; configured subagents: %s.", strings.Join(t.names, ", "))
		subagentType.Required = true
	}
	return &schema.ToolInfo{Name: "task", Desc: description, ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"subagent_type": subagentType, "description": {Type: schema.String, Required: true}})}, nil
}
func (t *taskTool) InvokableRun(ctx context.Context, raw string, _ ...einotool.Option) (string, error) {
	if t.runner == nil {
		return "", fmt.Errorf("child runner is required")
	}
	request, err := t.parseChildRequest(raw)
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

func (t *taskTool) parseChildRequest(raw string) (ChildRequest, error) {
	var input struct {
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		Task         string `json:"task"`
	}
	err := json.Unmarshal([]byte(raw), &input)
	if err != nil {
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
		if len(t.names) > 0 && !slices.Contains(t.names, "general-purpose") {
			return ChildRequest{}, fmt.Errorf("subagent_type is required; configured subagents: %s", strings.Join(t.names, ", "))
		}
		name = "general-purpose"
	}
	return ChildRequest{Name: name, Prompt: prompt}, nil
}
