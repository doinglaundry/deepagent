package middlewares

// TodoInstruction is the plan-mode preamble appended to the system
// prompt when plan mode is on. PlanReminder middleware injects it
// per-turn at runtime; constant lives here because TodoReminder also
// references the <plan_mode> tag for idempotency.
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
