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

// ImageFetcher is the data-plane dependency for ViewImage. Declared here
// to avoid an import cycle (agent imports this package).
type ImageFetcher interface {
	ReadImage(ctx context.Context, path string) ([]byte, string, error)
}

// ViewImage handles `view_image(path)` tool calls: fetches the bytes,
// appends a multimodal User message with the image, and rewrites the
// matching tool result to a short placeholder.
type ViewImage struct {
	*adk.BaseChatModelAgentMiddleware

	ViewImageToolName string
	Fetcher           ImageFetcher
	MaxBytes          int64
	Detail            schema.ImageURLDetail
	Logger            *slog.Logger
}

// NewViewImage returns a ViewImage middleware (8 MiB cap, auto detail).
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
	toolCallsCtx *adk.ToolCallsContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || toolCallsCtx == nil || len(toolCallsCtx.ToolCalls) == 0 {
		return ctx, state, nil
	}

	assistantIdx := lastAssistantWithToolCalls(state.Messages)
	if assistantIdx < 0 {
		return ctx, state, nil
	}
	assistant := state.Messages[assistantIdx]

	argsByID := make(map[string]string, len(assistant.ToolCalls))
	for _, c := range assistant.ToolCalls {
		argsByID[c.ID] = c.Function.Arguments
	}

	parts := make([]schema.MessageInputPart, 0)
	for _, call := range toolCallsCtx.ToolCalls {
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

	// Prepend a caption so vision models see a labeled input (Anthropic recommends this).
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

// lastAssistantWithToolCalls returns the index of the most recent assistant
// message with non-empty ToolCalls, or -1.
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

// rewriteToolMessage replaces the Content of the most recent Tool message
// with the given ToolCallID; no-op if none found.
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

// parseViewImageArgs extracts path/file_path/url from JSON args.
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
