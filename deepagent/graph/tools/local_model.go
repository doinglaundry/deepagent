package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"eino-cli/deepagent/localmodel"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type localModelTool struct {
	localModelService *localmodel.Service
}

func NewLocalModelTool(localModelService *localmodel.Service) agentmodel.ToolDescriptor {
	return agentmodel.ToolDescriptor{Tool: &localModelTool{localModelService: localModelService}, ReadOnly: true}
}

func (*localModelTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        "ask_local_model",
		Desc:        "Ask the local personal model about learned preferences or request a small draft. This returns one answer, does not train, and cannot use tools. Treat its answer as fallible context; verify facts using other tools. If the local model is busy, continue answering yourself without retrying it for this request.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"prompt": {Type: schema.String, Required: true}}),
	}, nil
}

func (localModelTool *localModelTool) InvokableRun(ctx context.Context, argumentsJSON string, _ ...tool.Option) (string, error) {
	var toolInput struct {
		Prompt string `json:"prompt"`
	}
	err := json.Unmarshal([]byte(argumentsJSON), &toolInput)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(toolInput.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	answer, err := localModelTool.localModelService.Generate(ctx, toolInput.Prompt)
	if errors.Is(err, localmodel.ErrModelBusy) {
		// 忙碌属于正常回退，不应终止当前 Graph 执行。
		return "Local model is busy with training or inference. Continue answering this request yourself; do not retry this tool for this request.", nil
	}
	if err != nil {
		return "", &agentmodel.InternalError{Err: err}
	}
	return answer, nil
}
