package tools

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/consts"
)

const askClarificationToolDesc = `Ask the user for clarification when required information is missing, ambiguous, risky, or needs an explicit choice before continuing. Use this before taking action; execution will be stopped by middleware and the question will be shown to the user.`

type clarificationArgs struct {
	Question          string   `json:"question" jsonschema:"required,description=The specific clarification question to ask the user"`
	ClarificationType string   `json:"clarification_type" jsonschema:"required,description=The type of clarification: missing_info, ambiguous_requirement, approach_choice, risk_confirmation, or suggestion"`
	Context           string   `json:"context,omitempty" jsonschema:"description=Optional context explaining why this clarification is needed"`
	Options           []string `json:"options,omitempty" jsonschema:"description=Optional choices for the user to pick from"`
}

// GetAskClarificationTool returns the "ask_clarification" tool. The function
// body is only a fallback; Clarification middleware intercepts the tool call.
func GetAskClarificationTool() (tool.BaseTool, error) {
	return utils.InferTool(consts.AskClarificationToolName, askClarificationToolDesc,
		func(ctx context.Context, in clarificationArgs) (string, error) {
			return "Clarification request processed by middleware", nil
		})
}
