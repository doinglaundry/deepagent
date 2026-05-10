package middlewares

import (
	"github.com/cloudwego/eino/adk"
)

// TodoInstruction is appended to the system prompt when plan mode is on.
const TodoInstruction = `

<plan_mode>
Plan mode is ON. Use the write_todos tool to maintain a single source of
truth for the task plan:

- Mark a todo "in_progress" BEFORE you start working on it.
- Mark it "completed" IMMEDIATELY after finishing — do not batch.
- Keep at most one task in_progress at a time unless they truly run in
  parallel.
- Update the list as you discover new sub-tasks; remove ones that
  become irrelevant.
- Skip the tool entirely for trivial requests (< 3 steps); using it for
  small tasks wastes tokens and clutters the user view.
</plan_mode>`

// NewTodo returns the AgentMiddleware that adds the plan-mode preamble.
// Only attach when RuntimeContext.IsPlanMode is true.
func NewTodo() adk.AgentMiddleware {
	return adk.AgentMiddleware{
		AdditionalInstruction: TodoInstruction,
	}
}
