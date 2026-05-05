package middlewares

import (
	"github.com/cloudwego/eino/adk"
)

// TodoInstruction is appended to the agent's system prompt when the host
// is running in plan mode. Mirrors the deerflow TodoMiddleware system
// prompt but condensed: deerflow ships ~80 lines of guidance and a
// matching tool description; we trust the prompt to convey the same
// intent in a fraction of the tokens.
//
// The write_todos tool itself is always available (deep.Config.
// WithoutWriteTodos = false in MakeLeadAgent); this instruction just
// nudges the model to actually use it when plan mode is on.
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

// NewTodo returns the AgentMiddleware (struct-based) that adds the
// plan-mode preamble to the system prompt. We use AgentMiddleware here
// instead of ChatModelAgentMiddleware because the only effect is a
// static instruction addition — exactly what AgentMiddleware was
// designed for.
//
// Only attach when RuntimeContext.IsPlanMode is true. The write_todos
// tool itself is wired by deep.Config.WithoutWriteTodos = false and is
// available regardless of plan mode.
func NewTodo() adk.AgentMiddleware {
	return adk.AgentMiddleware{
		AdditionalInstruction: TodoInstruction,
	}
}
