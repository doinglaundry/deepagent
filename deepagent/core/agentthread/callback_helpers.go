package agentthread

import (
	"context"
	deepagents "eino-cli/deepagent/core"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"strings"
)

// callbackInputArgs and callbackOutputText normalize Eino callback payloads
// before they become stable agentthread event fields.
func callbackInputArgs(input *tool.CallbackInput) string {
	if input == nil {
		return ""
	}
	return input.ArgumentsInJSON
}

func callbackOutputText(output *tool.CallbackOutput) string {
	if output == nil {
		return ""
	}
	if output.Response != "" {
		return output.Response
	}
	if output.ToolOutput != nil {
		var sb strings.Builder
		for _, part := range output.ToolOutput.Parts {
			if part.Text != "" {
				sb.WriteString(part.Text)
			}
		}
		return sb.String()
	}
	return ""
}

func toolCallbackCallID(ctx context.Context, input *tool.CallbackInput, output *tool.CallbackOutput) string {
	if callID := compose.GetToolCallID(ctx); callID != "" {
		return callID
	}
	if input != nil {
		if callID := toolCallbackExtraCallID(input.Extra); callID != "" {
			return callID
		}
	}
	if output != nil {
		if callID := toolCallbackExtraCallID(output.Extra); callID != "" {
			return callID
		}
	}
	return ""
}

func toolCallbackExtraCallID(extra map[string]any) string {
	if extra == nil {
		return ""
	}
	raw, ok := extra["tool_call_id"]
	if !ok {
		return ""
	}
	callID, _ := raw.(string)
	return callID
}

func eventLocationFromContext(ctx context.Context) EventLocation {
	agent := deepagents.GetDeepAgent(ctx)
	if agent == nil {
		return EventLocation{}
	}
	return EventLocation{AgentName: agent.Name(), AgentDepth: agent.Depth()}
}
