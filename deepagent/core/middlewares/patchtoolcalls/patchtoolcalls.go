package patchtoolcalls

import (
	"eino-cli/deepagent/core/middlewares"
	"github.com/cloudwego/eino/schema"
)

const interruptedToolResult = "Tool execution interrupted; external side effects may have occurred."

func New() middleware.Middleware { return &middleware.BaseMiddleware{} }

// PatchDanglingToolCalls closes assistant/tool pairs left incomplete by a
// crashed or interrupted Worker. The repaired copy is model-visible only; the
// durable original remains an accurate record of what was persisted.
func PatchDanglingToolCalls(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(messages))
	pending := make(map[string]schema.ToolCall)
	order := make([]string, 0)
	flush := func() {
		for _, id := range order {
			call, exists := pending[id]
			if !exists {
				continue
			}
			result = append(result, schema.ToolMessage(interruptedToolResult, id, schema.WithToolName(call.Function.Name)))
		}
		clear(pending)
		order = order[:0]
	}
	for _, message := range messages {
		if message == nil {
			continue
		}
		if len(pending) > 0 && message.Role != schema.Tool {
			flush()
		}
		result = append(result, message)
		switch message.Role {
		case schema.Assistant:
			for _, call := range message.ToolCalls {
				if call.ID == "" {
					continue
				}
				pending[call.ID] = call
				order = append(order, call.ID)
			}
		case schema.Tool:
			delete(pending, message.ToolCallID)
		}
	}
	flush()
	return result
}
