package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type FollowUpInfo struct {
	Question, UserAnswer string
	Questions            []string
}

func init() {
	schema.RegisterName[*FollowUpInfo]("deepagent_follow_up_info")
}

func GetFollowUpTool() ToolDescriptor {
	return ToolDescriptor{Tool: &followUpTool{}, ReadOnly: true}
}

type followUpTool struct{}

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
	isResumeTarget, hasFollowUpInfo, followUpInfo := tool.GetResumeContext[*FollowUpInfo](ctx)
	// 当前工具未被指定续跑时，提出问题并暂停。
	if !isResumeTarget {
		questionInfo, err := normalizeFollowUpArgs(arguments)
		if err != nil {
			return "", err
		}
		return "", tool.Interrupt(ctx, questionInfo)
	}

	// 当前工具续跑时，校验并返回用户回答。
	if !hasFollowUpInfo || followUpInfo == nil || strings.TrimSpace(followUpInfo.UserAnswer) == "" {
		return "", errors.New("follow-up resume requires an answer")
	}
	return followUpInfo.UserAnswer, nil
}

func normalizeFollowUpArgs(arguments string) (*FollowUpInfo, error) {
	var input struct {
		Question string          `json:"question"`
		Prompt   string          `json:"prompt"`
		Message  string          `json:"message"`
		Context  string          `json:"context"`
		Options  json.RawMessage `json:"options"`
	}
	inputDecodeErr := json.Unmarshal([]byte(arguments), &input)
	if inputDecodeErr != nil {
		return nil, inputDecodeErr
	}
	info := &FollowUpInfo{}
	for _, question := range []string{input.Question, input.Prompt, input.Message} {
		question = strings.TrimSpace(question)
		if question != "" {
			info.Question = question
			break
		}
	}
	if info.Question == "" {
		return nil, errors.New("question is required")
	}
	detail := strings.TrimSpace(input.Context)
	if detail != "" {
		info.Question = detail + "\n\n" + info.Question
	}
	if len(input.Options) > 0 && string(input.Options) != "null" {
		var options []string
		err := json.Unmarshal(input.Options, &options)
		if err != nil {
			var encoded string
			err := json.Unmarshal(input.Options, &encoded)
			if err != nil {
				return nil, errors.New("options must be strings")
			}
			decodeErr := json.Unmarshal([]byte(encoded), &options)
			if decodeErr != nil {
				options = []string{encoded}
			}
		}
		for _, option := range options {
			option = strings.TrimSpace(option)
			if option != "" {
				info.Questions = append(info.Questions, option)
			}
		}
	}
	return info, nil
}

type PlanStep = types.PlanStep

const ToolUpdatePlan = "update_plan"

type PlanUpdate struct {
	Plan        []PlanStep `json:"plan"`
	Explanation string     `json:"explanation,omitempty"`
}

type PlanUpdateHandler func(context.Context, PlanUpdate) error

type updatePlanTool struct{ onUpdate PlanUpdateHandler }

func NewUpdatePlanTool(onUpdate PlanUpdateHandler) ToolDescriptor {
	return ToolDescriptor{Tool: &updatePlanTool{onUpdate: onUpdate}, ReadOnly: true}
}

func (*updatePlanTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: ToolUpdatePlan, Desc: "Publish the current plan and progress.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"explanation": {Type: schema.String},
		"plan": {Type: schema.Array, Required: true, ElemInfo: &schema.ParameterInfo{Type: schema.Object, SubParams: map[string]*schema.ParameterInfo{
			"step":   {Type: schema.String, Required: true},
			"status": {Type: schema.String, Required: true, Enum: []string{"pending", "in_progress", "completed"}},
		}}},
	})}, nil
}

func (t *updatePlanTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	update, err := normalizePlanArgs(raw)
	if err != nil {
		return "", err
	}
	contextErr := ctx.Err()
	if contextErr != nil {
		return "", contextErr
	}
	if t.onUpdate != nil {
		err := t.onUpdate(ctx, update)
		if err != nil {
			return "", err
		}
	}
	state := types.RunStateFromContext(ctx)
	if state != nil {
		state.Plan = append([]types.PlanStep(nil), update.Plan...)
	}
	encoded, err := json.Marshal(update)
	return string(encoded), err
}

func normalizePlanArgs(raw string) (PlanUpdate, error) {
	var object struct {
		Plan  json.RawMessage `json:"plan"`
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
		Explanation string `json:"explanation"`
	}
	var update PlanUpdate
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return update, errors.New("plan is required")
	}
	if raw[0] != '{' {
		text := raw
		if raw[0] == '"' {
			err := json.Unmarshal([]byte(raw), &text)
			if err != nil {
				return update, err
			}
		}
		if raw[0] == '[' {
			return update, errors.New("plan must be an object or plain text")
		}
		update.Plan = []PlanStep{{Step: text, Status: "in_progress"}}
	} else {
		decodeErr := json.Unmarshal([]byte(raw), &object)
		if decodeErr != nil {
			return update, decodeErr
		}
		update.Explanation = object.Explanation
		// Legacy todos takes precedence over a legacy plain-text plan.
		if len(object.Todos) > 0 {
			for _, todo := range object.Todos {
				update.Plan = append(update.Plan, PlanStep{Step: todo.Content, Status: todo.Status})
			}
		} else if len(object.Plan) > 0 && object.Plan[0] == '"' {
			var text string
			err := json.Unmarshal(object.Plan, &text)
			if err != nil {
				return update, err
			}
			update.Plan = []PlanStep{{Step: text, Status: "in_progress"}}
		} else {
			err := json.Unmarshal(object.Plan, &update.Plan)
			if err != nil {
				return update, err
			}
		}
	}
	if len(update.Plan) == 0 {
		return update, errors.New("plan is required")
	}
	for _, step := range update.Plan {
		if strings.TrimSpace(step.Step) == "" || (step.Status != "pending" && step.Status != "in_progress" && step.Status != "completed") {
			return update, errors.New("invalid plan step")
		}
	}
	return update, nil
}
