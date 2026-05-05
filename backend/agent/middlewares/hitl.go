package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// HITL ("human-in-the-loop") mirrors
// deerflow.agents.middlewares.human_in_the_loop_middleware. The Python
// version pauses the agent before specific tool calls (e.g. shell.execute,
// fs.write) so the user can approve / deny the action.
//
// Phase 3 wires the detection skeleton: Tools is the set of tool names that
// require approval, ApprovalCallback is consulted for each one. Approval
// flow integration with the REPL (returning adk.CancelError + persisting a
// resume checkpoint) lands once the REPL gains an approval prompt UI.
type HITL struct {
	*adk.BaseChatModelAgentMiddleware

	// Tools is the set of tool names that require approval before
	// execution. Empty means no tool requires approval.
	Tools map[string]struct{}

	// ApprovalCallback is consulted for each gated tool call. Returning
	// false aborts the run; nil treats every call as approved (the Phase 3
	// default until the REPL UI lands).
	ApprovalCallback func(ctx context.Context, toolName, args string) bool

	Logger *slog.Logger
}

// NewHITL returns a Human-in-the-Loop middleware. Pass the tool-name
// allowlist and an ApprovalCallback wired to the host's prompt UI.
func NewHITL(tools []string, cb func(context.Context, string, string) bool) *HITL {
	set := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		set[t] = struct{}{}
	}
	return &HITL{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Tools:                        set,
		ApprovalCallback:             cb,
		Logger:                       slog.Default(),
	}
}

func (m *HITL) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 || len(m.Tools) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last == nil || last.Role != schema.Assistant {
		return ctx, state, nil
	}
	for _, call := range last.ToolCalls {
		if _, gated := m.Tools[call.Function.Name]; !gated {
			continue
		}
		approved := true
		if m.ApprovalCallback != nil {
			approved = m.ApprovalCallback(ctx, call.Function.Name, call.Function.Arguments)
		}
		if !approved {
			m.Logger.Warn("HITL: tool call denied by approval callback",
				"tool", call.Function.Name, "call_id", call.ID)
			// TODO(phase4+): emit adk.CancelError-equivalent so the runner
			// pauses with a checkpoint instead of executing the call.
		}
	}
	return ctx, state, nil
}
