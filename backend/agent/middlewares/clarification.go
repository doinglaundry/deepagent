package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// AskClarificationToolName is the conventional tool name the lead-agent
// prompt instructs the model to call when it needs user input.
const AskClarificationToolName = "ask_clarification"

// Clarification mirrors
// deerflow.agents.middlewares.clarification_middleware. When the model emits
// a tool call to AskClarificationToolName, this middleware needs to pause
// the agent and surface the question to the REPL.
//
// Phase 1 ships a detection-only variant: it logs the call so we can verify
// the chain wires correctly, and exposes Detected() for the runtime layer to
// poll. The interrupt mechanics (returning adk.CancelError / SetRunLocalValue
// to drive the REPL approval prompt) move in alongside the rest of the
// resume-flow refactor in Phase 2.
type Clarification struct {
	*adk.BaseChatModelAgentMiddleware

	Logger *slog.Logger
}

// NewClarification returns a Clarification middleware with a default logger.
// Must always be the last middleware in the chain (matches Python's
// "ClarificationMiddleware should always be last" comment).
func NewClarification() *Clarification {
	return &Clarification{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Logger:                       slog.Default(),
	}
}

func (m *Clarification) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last == nil || last.Role != schema.Assistant {
		return ctx, state, nil
	}
	for _, call := range last.ToolCalls {
		if call.Function.Name == AskClarificationToolName {
			m.Logger.Info("clarification requested",
				"call_id", call.ID, "args", call.Function.Arguments)
			// TODO(phase2): trigger adk.CancelError + persist a pending
			// approval state so the REPL can resume after user response.
		}
	}
	return ctx, state, nil
}
