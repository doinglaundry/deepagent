package middlewares

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// HITL ("human-in-the-loop") mirrors
// deerflow.agents.middlewares.human_in_the_loop_middleware.
//
// Real control flow (no eino interrupt API needed):
//   - Tools whose names appear in HITL.Tools require approval BEFORE the
//     deep-agent dispatches them.
//   - When the model emits an assistant message containing one or more
//     gated calls, the middleware invokes ApprovalCallback synchronously
//     for each one. Because middleware hooks block the run, the REPL has
//     time to prompt the user on stdin.
//   - If the callback returns false, the gated tool call is REMOVED from
//     last.ToolCalls and a "denied" tool-result message is appended to
//     state.Messages so the next model turn knows what happened. If every
//     tool call in the message was denied, last.Content is set to a
//     placeholder so the agent loop can terminate cleanly with a visible
//     assistant turn.
//
// Approved calls are left untouched and execute via the normal pipeline.
type HITL struct {
	*adk.BaseChatModelAgentMiddleware

	// Tools is the set of tool names that require approval before
	// execution. Empty means no tool requires approval.
	Tools map[string]struct{}

	// ApprovalCallback is consulted for each gated tool call. Returning
	// false denies the call. nil treats every call as approved.
	ApprovalCallback func(ctx context.Context, toolName, args string) bool

	Logger *slog.Logger
}

// NewHITL returns a Human-in-the-Loop middleware. Pass the tool-name
// allowlist and an ApprovalCallback wired to the host's prompt UI.
func NewHITL(tools []string, cb func(context.Context, string, string) bool) *HITL {
	set := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
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
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 || len(m.Tools) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last == nil || last.Role != schema.Assistant || len(last.ToolCalls) == 0 {
		return ctx, state, nil
	}

	kept := make([]schema.ToolCall, 0, len(last.ToolCalls))
	deniedMessages := make([]*schema.Message, 0)
	denied := 0

	for _, call := range last.ToolCalls {
		if _, gated := m.Tools[call.Function.Name]; !gated {
			kept = append(kept, call)
			continue
		}
		approved := true
		if m.ApprovalCallback != nil {
			approved = m.ApprovalCallback(ctx, call.Function.Name, call.Function.Arguments)
		}
		if approved {
			kept = append(kept, call)
			continue
		}
		denied++
		m.Logger.Warn("HITL: tool call denied by approval callback",
			"tool", call.Function.Name, "call_id", call.ID)
		deniedMessages = append(deniedMessages, &schema.Message{
			Role:       schema.Tool,
			ToolCallID: call.ID,
			ToolName:   call.Function.Name,
			Content: fmt.Sprintf(
				"User denied the call to %q. Do not retry; respond to the user explaining the situation or proceeding without that tool.",
				call.Function.Name),
		})
	}

	if denied == 0 {
		return ctx, state, nil
	}

	last.ToolCalls = kept
	if len(kept) == 0 && strings.TrimSpace(last.Content) == "" {
		last.Content = "(tool execution denied by user — the agent will respond without invoking the requested tools)"
	}

	// Inject synthetic tool messages so the next model turn knows the
	// outcome without a dangling tool_call_id.
	state.Messages = append(state.Messages, deniedMessages...)
	return ctx, state, nil
}
