package middlewares

import (
	"github.com/cloudwego/eino/adk"
)

// TodoInstruction is appended to the agent's system prompt when the host is
// running in plan mode. Mirrors the deerflow PlanMode addendum but kept
// deliberately short — the full deerflow prompt section will be ported when
// the planner backend lands.
const TodoInstruction = `

<plan_mode>
Plan mode is active. Use the write_todos tool to keep a single source of
truth for the task plan. Update the list before and after every meaningful
step so the user can follow the progress.
</plan_mode>`

// NewTodo returns the AgentMiddleware (struct-based) that adds the plan-mode
// preamble to the system prompt. We use AgentMiddleware here instead of
// ChatModelAgentMiddleware because the only effect is a static instruction
// addition — exactly what AgentMiddleware was designed for.
//
// Only attach when RuntimeContext.IsPlanMode is true.
func NewTodo() adk.AgentMiddleware {
	return adk.AgentMiddleware{
		AdditionalInstruction: TodoInstruction,
	}
}
