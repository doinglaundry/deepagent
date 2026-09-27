package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ApprovalResult struct {
	CallID           string
	Approved         bool
	DisapproveReason *string
}

func GetFollowUpTool() tool.BaseTool { return &followUpTool{} }

type followUpTool struct{}

func (*followUpTool) ReadOnly() bool { return true }

func (*followUpTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "ask_user",
		Desc: "Pause and ask the user one question. Execution resumes with the user's answer.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"question": {Type: schema.String, Required: true},
			"options":  {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
		}),
	}, nil
}

func (*followUpTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	if target, hasData, resumed := tool.GetResumeContext[*FollowUpInfo](ctx); target {
		if !hasData || resumed == nil || strings.TrimSpace(resumed.UserAnswer) == "" {
			return "", errors.New("follow-up resume requires an answer")
		}
		return resumed.UserAnswer, nil
	}
	info, err := normalizeFollowUpArgs(arguments)
	if err != nil {
		return "", err
	}
	return "", tool.Interrupt(ctx, info)
}

func normalizeFollowUpArgs(arguments string) (*FollowUpInfo, error) {
	var input struct {
		Question string          `json:"question"`
		Prompt   string          `json:"prompt"`
		Message  string          `json:"message"`
		Context  string          `json:"context"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return nil, err
	}
	info := &FollowUpInfo{}
	for _, question := range []string{input.Question, input.Prompt, input.Message} {
		if question = strings.TrimSpace(question); question != "" {
			info.Question = question
			break
		}
	}
	if info.Question == "" {
		return nil, errors.New("question is required")
	}
	if detail := strings.TrimSpace(input.Context); detail != "" {
		info.Question = detail + "\n\n" + info.Question
	}
	if len(input.Options) > 0 && string(input.Options) != "null" {
		var options []string
		if err := json.Unmarshal(input.Options, &options); err != nil {
			var encoded string
			if err := json.Unmarshal(input.Options, &encoded); err != nil {
				return nil, errors.New("options must be strings")
			}
			if err := json.Unmarshal([]byte(encoded), &options); err != nil {
				options = []string{encoded}
			}
		}
		for _, option := range options {
			if option = strings.TrimSpace(option); option != "" {
				info.Questions = append(info.Questions, option)
			}
		}
	}
	return info, nil
}
