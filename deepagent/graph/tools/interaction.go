package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func NewFollowUpTool() agentmodel.ToolDescriptor {
	return agentmodel.ToolDescriptor{Tool: &followUpTool{}, ReadOnly: true}
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
	isResumeTarget, hasFollowUpInfo, followUpInfo := tool.GetResumeContext[*agentmodel.FollowUpInfo](ctx)
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

func normalizeFollowUpArgs(arguments string) (*agentmodel.FollowUpInfo, error) {
	var followUpArgs struct {
		Question string          `json:"question"`
		Prompt   string          `json:"prompt"`
		Message  string          `json:"message"`
		Context  string          `json:"context"`
		Options  json.RawMessage `json:"options"`
	}
	inputDecodeErr := json.Unmarshal([]byte(arguments), &followUpArgs)
	if inputDecodeErr != nil {
		return nil, inputDecodeErr
	}
	followUpInfo := &agentmodel.FollowUpInfo{}
	for _, question := range []string{followUpArgs.Question, followUpArgs.Prompt, followUpArgs.Message} {
		question = strings.TrimSpace(question)
		if question != "" {
			followUpInfo.Question = question
			break
		}
	}
	if followUpInfo.Question == "" {
		return nil, errors.New("question is required")
	}
	contextText := strings.TrimSpace(followUpArgs.Context)
	if contextText != "" {
		followUpInfo.Question = contextText + "\n\n" + followUpInfo.Question
	}
	if len(followUpArgs.Options) > 0 && string(followUpArgs.Options) != "null" {
		var options []string
		err := json.Unmarshal(followUpArgs.Options, &options)
		if err != nil {
			var encodedOptions string
			err := json.Unmarshal(followUpArgs.Options, &encodedOptions)
			if err != nil {
				return nil, errors.New("options must be strings")
			}
			decodeErr := json.Unmarshal([]byte(encodedOptions), &options)
			if decodeErr != nil {
				options = []string{encodedOptions}
			}
		}
		for _, option := range options {
			option = strings.TrimSpace(option)
			if option != "" {
				followUpInfo.Questions = append(followUpInfo.Questions, option)
			}
		}
	}
	return followUpInfo, nil
}

const ToolUpdatePlan = "update_plan"

type updatePlanTool struct{ onUpdate agentmodel.PlanUpdateHandler }

func NewUpdatePlanTool(onUpdate agentmodel.PlanUpdateHandler) agentmodel.ToolDescriptor {
	return agentmodel.ToolDescriptor{Tool: &updatePlanTool{onUpdate: onUpdate}, ReadOnly: true}
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

func (updatePlanTool *updatePlanTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	planUpdate, err := normalizePlanArgs(arguments)
	if err != nil {
		return "", err
	}
	contextErr := ctx.Err()
	if contextErr != nil {
		return "", contextErr
	}
	if updatePlanTool.onUpdate != nil {
		err := updatePlanTool.onUpdate(ctx, planUpdate)
		if err != nil {
			return "", err
		}
	}
	runState := agentmodel.GetRunState(ctx)
	if runState != nil {
		runState.Plan = append([]agentmodel.PlanStep(nil), planUpdate.Plan...)
	}
	encodedPlanUpdate, err := json.Marshal(planUpdate)
	return string(encodedPlanUpdate), err
}

func normalizePlanArgs(arguments string) (agentmodel.PlanUpdate, error) {
	var planArgs struct {
		Plan  json.RawMessage `json:"plan"`
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
		Explanation string `json:"explanation"`
	}
	var planUpdate agentmodel.PlanUpdate
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return planUpdate, errors.New("plan is required")
	}
	if arguments[0] != '{' {
		planText := arguments
		if arguments[0] == '"' {
			err := json.Unmarshal([]byte(arguments), &planText)
			if err != nil {
				return planUpdate, err
			}
		}
		if arguments[0] == '[' {
			return planUpdate, errors.New("plan must be an object or plain text")
		}
		planUpdate.Plan = []agentmodel.PlanStep{{Step: planText, Status: "in_progress"}}
	} else {
		decodeErr := json.Unmarshal([]byte(arguments), &planArgs)
		if decodeErr != nil {
			return planUpdate, decodeErr
		}
		planUpdate.Explanation = planArgs.Explanation
		// Legacy todos takes precedence over a legacy plain-text plan.
		if len(planArgs.Todos) > 0 {
			for _, todo := range planArgs.Todos {
				planUpdate.Plan = append(planUpdate.Plan, agentmodel.PlanStep{Step: todo.Content, Status: todo.Status})
			}
		} else if len(planArgs.Plan) > 0 && planArgs.Plan[0] == '"' {
			var planText string
			err := json.Unmarshal(planArgs.Plan, &planText)
			if err != nil {
				return planUpdate, err
			}
			planUpdate.Plan = []agentmodel.PlanStep{{Step: planText, Status: "in_progress"}}
		} else {
			err := json.Unmarshal(planArgs.Plan, &planUpdate.Plan)
			if err != nil {
				return planUpdate, err
			}
		}
	}
	if len(planUpdate.Plan) == 0 {
		return planUpdate, errors.New("plan is required")
	}
	for _, step := range planUpdate.Plan {
		if strings.TrimSpace(step.Step) == "" || (step.Status != "pending" && step.Status != "in_progress" && step.Status != "completed") {
			return planUpdate, errors.New("invalid plan step")
		}
	}
	return planUpdate, nil
}
