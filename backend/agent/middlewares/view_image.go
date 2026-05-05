package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"
)

// ViewImage mirrors deerflow.agents.middlewares.view_image_middleware. The
// Python version intercepts a `view_image` tool call and rewrites the
// resulting ToolMessage into a multimodal user message so the next model
// call can ingest the image directly.
//
// Phase 3 ships the detection skeleton — we count occurrences and log
// them. The real rewrite (binding image bytes via UserInputMultiContent)
// lands once the sandbox layer in Phase 4 surfaces a stable image-fetch
// API.
type ViewImage struct {
	*adk.BaseChatModelAgentMiddleware

	// ViewImageToolName is the conventional tool name agents call to fetch
	// an image; defaults to "view_image" matching the deerflow prompt.
	ViewImageToolName string

	Logger *slog.Logger
}

// NewViewImage returns a ViewImage middleware. Only attach it when the
// active model has SupportsVision=true.
func NewViewImage() *ViewImage {
	return &ViewImage{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		ViewImageToolName:            "view_image",
		Logger:                       slog.Default(),
	}
}

func (m *ViewImage) AfterToolCallsRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	tc *adk.ToolCallsContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if tc == nil {
		return ctx, state, nil
	}
	for _, call := range tc.ToolCalls {
		if call.Name == m.ViewImageToolName {
			m.Logger.Debug("view_image tool call observed",
				"call_id", call.CallID)
			// TODO(phase4+): rewrite the trailing schema.Tool message into
			// a User message with UserInputMultiContent carrying the image
			// bytes pulled via the sandbox provider.
			_ = state
		}
	}
	return ctx, state, nil
}
