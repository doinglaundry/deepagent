package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// SubagentLimit truncates parallel task() tool calls to MaxParallel per turn.
type SubagentLimit struct {
	*adk.BaseChatModelAgentMiddleware

	TaskToolName string
	MaxParallel  int
	Logger       *slog.Logger
}

// NewSubagentLimit returns a SubagentLimit middleware (default MaxParallel=3).
func NewSubagentLimit(maxParallel int) *SubagentLimit {
	if maxParallel <= 0 {
		maxParallel = 3
	}
	return &SubagentLimit{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		TaskToolName:                 "task",
		MaxParallel:                  maxParallel,
		Logger:                       slog.Default(),
	}
}

func (m *SubagentLimit) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last == nil || last.Role != schema.Assistant || len(last.ToolCalls) == 0 {
		return ctx, state, nil
	}

	taskCount := 0
	for _, call := range last.ToolCalls {
		if call.Function.Name == m.TaskToolName {
			taskCount++
		}
	}
	if taskCount <= m.MaxParallel {
		return ctx, state, nil
	}

	kept := make([]schema.ToolCall, 0, len(last.ToolCalls))
	keptTasks := 0
	dropped := 0
	for _, call := range last.ToolCalls {
		if call.Function.Name == m.TaskToolName {
			if keptTasks >= m.MaxParallel {
				dropped++
				continue
			}
			keptTasks++
		}
		kept = append(kept, call)
	}
	last.ToolCalls = kept
	m.Logger.Warn("subagent-limit: truncated parallel task() calls",
		"limit", m.MaxParallel, "requested", taskCount, "dropped", dropped)
	return ctx, state, nil
}
