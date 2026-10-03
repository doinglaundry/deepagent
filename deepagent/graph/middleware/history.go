package middleware

import (
	"context"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/schema"
)

const interruptedToolResult = "Tool execution interrupted; external side effects may have occurred."

type patchToolCalls struct{ BaseMiddleware }

func NewPatchToolCalls() Middleware { return &patchToolCalls{} }

func (*patchToolCalls) GetName() string { return "patch_tool_calls" }

func (*patchToolCalls) ModifyModelRequest(_ context.Context, _ []*schema.Message, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
	return PatchDanglingToolCalls(messages), nil
}

// PatchDanglingToolCalls closes assistant/tool pairs left incomplete by a
// crashed or interrupted Worker. The repaired copy is model-visible only; the
// durable original remains an accurate record of what was persisted.
func PatchDanglingToolCalls(messages []*schema.Message) []*schema.Message {
	patchedMessages := make([]*schema.Message, 0, len(messages))
	pendingCalls := make(map[string]schema.ToolCall)
	pendingCallIDs := make([]string, 0)
	appendInterruptedResults := func() {
		for _, callID := range pendingCallIDs {
			toolCall, exists := pendingCalls[callID]
			if !exists {
				continue
			}
			patchedMessages = append(patchedMessages, schema.ToolMessage(interruptedToolResult, callID, schema.WithToolName(toolCall.Function.Name)))
		}
		clear(pendingCalls)
		pendingCallIDs = pendingCallIDs[:0]
	}
	for _, message := range messages {
		if message == nil {
			continue
		}
		if len(pendingCalls) > 0 && message.Role != schema.Tool {
			appendInterruptedResults()
		}
		patchedMessages = append(patchedMessages, message)
		switch message.Role {
		case schema.Assistant:
			for _, toolCall := range message.ToolCalls {
				if toolCall.ID == "" {
					continue
				}
				pendingCalls[toolCall.ID] = toolCall
				pendingCallIDs = append(pendingCallIDs, toolCall.ID)
			}
		case schema.Tool:
			delete(pendingCalls, message.ToolCallID)
		}
	}
	appendInterruptedResults()
	return patchedMessages
}
