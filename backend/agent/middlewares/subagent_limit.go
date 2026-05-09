package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// SubagentLimit mirrors the Python soft-limit guard documented in the
// lead-agent prompt: "MAX N task calls per response. Excess calls are
// silently discarded by the system." We enforce that on the assistant
// message itself by truncating ToolCalls before they fan out to execution.
type SubagentLimit struct {
	*adk.BaseChatModelAgentMiddleware

	// TaskToolName is the conventional name of the subagent dispatch tool;
	// defaults to "task" to match the deerflow prompt.
	TaskToolName string

	// MaxParallel caps the number of concurrent task() calls per turn.
	// Phase 1 default was 3; AppConfig surfaces an override.
	MaxParallel int

	Logger *slog.Logger
}

// NewSubagentLimit returns a SubagentLimit middleware. Only attach when
// RuntimeContext.SubagentEnabled is true.
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
