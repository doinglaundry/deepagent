package middlewares

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/schema"
)

// todoReminderTag is the idempotency anchor: every reminder we emit carries
// this exact substring so a second pass can detect "already injected for
// this turn" without a separate state field.
const todoReminderTag = `<system_reminder type="todo">`

// TodoReminder injects a system reminder when the in-flight todo list still
// lives in adk.SessionKeyTodos but the original write_todos AssistantMessage
// is no longer in state.Messages (typically because Summarization scrubbed
// it). Mirrors deer-flow's HumanMessage(name="todo_reminder") path; eino
// has no message-name discriminator, hence SystemMessage + an XML tag for
// idempotency.
type TodoReminder struct {
	*adk.BaseChatModelAgentMiddleware
}

func NewTodoReminder() *TodoReminder {
	return &TodoReminder{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}}
}

func (m *TodoReminder) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil {
		return ctx, state, nil
	}

	raw, ok := adk.GetSessionValue(ctx, deep.SessionKeyTodos)
	if !ok {
		return ctx, state, nil
	}
	todos, _ := raw.([]deep.TODO)
	state.Messages = injectTodoReminder(state.Messages, todos)
	return ctx, state, nil
}

// injectTodoReminder is the ctx-free decision core, split out so unit tests
// don't need an adk.Runner-managed ctx to drive the BeforeModel hook.
// Returns msgs unchanged when no injection is warranted, or a new slice
// with the reminder prepended.
//
// Skip rules:
//  1. todos empty → nothing to remind about.
//  2. hasWriteTodosCall(msgs) → model still sees its own write_todos call
//     in history (Assistant ToolCalls + ToolMessage pair); reminder is
//     redundant.
//  3. hasReminderTag(msgs) → we already injected one this turn; second
//     pass must be a no-op for replay / interrupt-resume idempotency.
func injectTodoReminder(msgs []*schema.Message, todos []deep.TODO) []*schema.Message {
	if len(todos) == 0 {
		return msgs
	}
	if hasWriteTodosCall(msgs) || hasReminderTag(msgs) {
		return msgs
	}
	return append([]*schema.Message{
		schema.SystemMessage(renderTodoReminder(todos)),
	}, msgs...)
}

func hasWriteTodosCall(msgs []*schema.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg == nil || msg.Role != schema.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == "write_todos" {
				return true
			}
		}
	}
	return false
}

func hasReminderTag(msgs []*schema.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg == nil || msg.Role != schema.System {
			continue
		}
		if strings.Contains(msg.Content, todoReminderTag) {
			return true
		}
	}
	return false
}

func renderTodoReminder(todos []deep.TODO) string {
	var sb strings.Builder
	sb.WriteString(todoReminderTag)
	sb.WriteString("\n")
	sb.WriteString("Your todo list from earlier is no longer in the visible context, but it is\n")
	sb.WriteString("still active. Current state:\n\n")
	for _, t := range todos {
		fmt.Fprintf(&sb, "- [%s] %s\n", t.Status, t.Content)
	}
	sb.WriteString("\nContinue tracking and updating this list as you work. Call `write_todos`\n")
	sb.WriteString("whenever a status changes.\n")
	sb.WriteString(`</system_reminder>`)
	return sb.String()
}
