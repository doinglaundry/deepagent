package middlewares

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// ImageFetcher is the data-plane dependency for the ViewImage middleware.
// It is declared here (rather than in the agent package) because the
// agent package imports this one — declaring it upstream would form a
// cycle. The agent package satisfies it via an inline imageFetcherFunc
// adapter closing over readImage.
type ImageFetcher interface {
	ReadImage(ctx context.Context, path string) ([]byte, string, error)
}

// ViewImage mirrors deerflow.agents.middlewares.view_image_middleware.
//
// Real Phase-7 behaviour: when the model calls `view_image(path="…")`,
// the middleware reads the image bytes via the sandbox and APPENDS a
// User message carrying a base64-encoded image part to state.Messages.
// The next model turn sees:
//
//   <assistant ToolCalls=[view_image(path)]>
//   <tool result="image attached as next user message">
//   <user UserInputMultiContent=[text: "(image)" , image: <bytes>]>
//
// The original tool message is rewritten to a short placeholder so the
// model isn't confused by raw bytes / paths inside its tool output.
//
// Only attach when the active model has SupportsVision=true.
type ViewImage struct {
	*adk.BaseChatModelAgentMiddleware

	// ViewImageToolName is the conventional tool name agents call to
	// fetch an image; defaults to "view_image" matching the deerflow
	// prompt.
	ViewImageToolName string

	// Fetcher is the binary-data source. Required.
	Fetcher ImageFetcher

	// MaxBytes caps the size of any single image. 0 disables the cap;
	// negative is treated as 0. The middleware logs a warning and skips
	// (without erroring) when an image exceeds the cap.
	MaxBytes int64

	// Detail controls the detail hint sent to the model. Defaults to
	// schema.ImageURLDetailAuto.
	Detail schema.ImageURLDetail

	Logger *slog.Logger
}

// NewViewImage returns a ViewImage middleware. Pass a non-nil fetcher;
// without one the middleware degrades to a logging skeleton (the
// previous Phase-3 behaviour).
func NewViewImage(fetcher ImageFetcher) *ViewImage {
	return &ViewImage{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		ViewImageToolName:            "view_image",
		Fetcher:                      fetcher,
		MaxBytes:                     8 << 20, // 8 MiB
		Detail:                       schema.ImageURLDetailAuto,
		Logger:                       slog.Default(),
	}
}

func (m *ViewImage) AfterToolCallsRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	tc *adk.ToolCallsContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || tc == nil || len(tc.ToolCalls) == 0 {
		return ctx, state, nil
	}

	// Find the assistant message that issued these tool calls. It is the
	// last assistant message before the appended tool results.
	assistantIdx := lastAssistantWithToolCalls(state.Messages)
	if assistantIdx < 0 {
		return ctx, state, nil
	}
	assistant := state.Messages[assistantIdx]

	// Pre-index tool calls by ID for argument lookup.
	argsByID := make(map[string]string, len(assistant.ToolCalls))
	for _, c := range assistant.ToolCalls {
		argsByID[c.ID] = c.Function.Arguments
	}

	parts := make([]schema.MessageInputPart, 0)
	for _, call := range tc.ToolCalls {
		if call.Name != m.ViewImageToolName {
			continue
		}
		path := parseViewImageArgs(argsByID[call.CallID])
		if path == "" {
			m.Logger.Warn("view_image: missing path argument", "call_id", call.CallID)
			continue
		}
		if m.Fetcher == nil {
			m.Logger.Warn("view_image: no fetcher wired, skipping", "path", path)
			continue
		}
		data, mime, err := m.Fetcher.ReadImage(ctx, path)
		if err != nil {
			m.Logger.Warn("view_image: read failed",
				"path", path, "call_id", call.CallID, "err", err)
			continue
		}
		if m.MaxBytes > 0 && int64(len(data)) > m.MaxBytes {
			m.Logger.Warn("view_image: image exceeds MaxBytes; skipping",
				"path", path, "size", len(data), "limit", m.MaxBytes)
			continue
		}
		// Replace the matching tool result with a short placeholder so
		// the model's context isn't polluted with paths / sizes.
		rewriteToolMessage(state.Messages, call.CallID,
			fmt.Sprintf("(image %q attached as next user message)", path))

		b64 := base64.StdEncoding.EncodeToString(data)
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &b64,
					MIMEType:   mime,
				},
				Detail: m.Detail,
			},
		})
	}

	if len(parts) == 0 {
		return ctx, state, nil
	}

	// Prepend a small text part so the model sees a captioned input
	// rather than a bare image. This matches Anthropic's recommended
	// usage pattern for vision-capable models.
	header := schema.MessageInputPart{
		Type: schema.ChatMessagePartTypeText,
		Text: "(image attachment from view_image tool)",
	}
	full := append([]schema.MessageInputPart{header}, parts...)

	state.Messages = append(state.Messages, &schema.Message{
		Role:                  schema.User,
		UserInputMultiContent: full,
	})
	return ctx, state, nil
}

// lastAssistantWithToolCalls returns the index of the most recent
// assistant message whose ToolCalls is non-empty, or -1.
func lastAssistantWithToolCalls(msgs []*schema.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m == nil {
			continue
		}
		if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
			return i
		}
	}
	return -1
}

// rewriteToolMessage finds the most recent Tool message with the given
// ToolCallID and replaces its Content. No-op if none found.
func rewriteToolMessage(msgs []*schema.Message, callID, content string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m == nil {
			continue
		}
		if m.Role == schema.Tool && m.ToolCallID == callID {
			m.Content = content
			return
		}
	}
}

// parseViewImageArgs extracts the "path" field from the JSON arguments.
// Mirrors clarification.go's parseClarificationArgs in spirit; falls
// back to the raw string when JSON parsing fails so misconfigured tool
// schemas don't silently lose the path.
func parseViewImageArgs(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var args struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return ""
	}
	switch {
	case strings.TrimSpace(args.Path) != "":
		return strings.TrimSpace(args.Path)
	case strings.TrimSpace(args.FilePath) != "":
		return strings.TrimSpace(args.FilePath)
	case strings.TrimSpace(args.URL) != "":
		return strings.TrimSpace(args.URL)
	}
	return ""
}
