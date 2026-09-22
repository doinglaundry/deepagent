package middlewares

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type HITL struct {
	*adk.BaseChatModelAgentMiddleware
	Tools            map[string]struct{}
	ApprovalCallback func(ctx context.Context, toolName, args string) bool
	Logger           *slog.Logger
}

// NewHITL returns a Human-in-the-Loop middleware. cb=nil treats every call as approved.
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
	lastMsg := state.Messages[len(state.Messages)-1]
	if lastMsg == nil || lastMsg.Role != schema.Assistant || len(lastMsg.ToolCalls) == 0 {
		return ctx, state, nil
	}

	kept := make([]schema.ToolCall, 0, len(lastMsg.ToolCalls))
	denied := 0

	for _, call := range lastMsg.ToolCalls {
		if _, exists := m.Tools[call.Function.Name]; !exists {
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
	}

	if denied == 0 {
		return ctx, state, nil
	}

	lastMsg.ToolCalls = kept
	denial := "Tool execution denied by user. Do not retry denied tools; respond explaining the situation or proceed without them."
	if strings.TrimSpace(lastMsg.Content) == "" {
		lastMsg.Content = denial
	} else {
		lastMsg.Content = strings.TrimSpace(lastMsg.Content) + "\n\n" + denial
	}
	return ctx, state, nil
}
