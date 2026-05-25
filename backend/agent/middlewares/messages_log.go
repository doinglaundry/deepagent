package middlewares

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/consts"
	runtimecontext "eino-cli/backend/runtime/context"
)

type MessagesLog struct {
	*adk.BaseChatModelAgentMiddleware
	path          string
	transcriptDir string
	seen          int
}

func NewMessagesLog(path, transcriptDir string) *MessagesLog {
	return &MessagesLog{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		path:                         path,
		transcriptDir:                transcriptDir,
	}
}

func (m *MessagesLog) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if runtimecontext.GetQuerySource(ctx) == runtimecontext.QuerySourceAutoDream {
		return ctx, state, nil
	}
	if m.path == "" || state == nil {
		return ctx, state, nil
	}
	start := m.seen
	if start > len(state.Messages) {
		start = 0
	}
	messages := state.Messages[start:]
	m.seen = len(state.Messages)
	if len(messages) == 0 {
		return ctx, state, nil
	}
	appendMessagesLog(m.path, messages)
	appendTranscriptLog(ctx, m.transcriptDir, messages)
	return ctx, state, nil
}

func appendMessagesLog(path string, messages []*schema.Message) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("messages log: mkdir failed", "path", path, "err", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("messages log: open failed", "path", path, "err", err)
		return
	}
	defer f.Close()

	for _, msg := range messages {
		if _, err := fmt.Fprint(f, formatMessageLogEntry(msg)); err != nil {
			slog.Warn("messages log: write failed", "path", path, "err", err)
			return
		}
	}
}

func formatMessageLogEntry(msg *schema.Message) string {
	role := ""
	content := ""
	if msg != nil {
		role = fmt.Sprint(msg.Role)
		content = getMessageLogContent(msg)
	}
	fence := markdownFence(content)
	return fmt.Sprintf("## %s · %s\n\n%stext\n%s\n%s\n\n---\n\n",
		time.Now().UTC().Format(time.RFC3339Nano),
		role,
		fence,
		content,
		fence,
	)
}

func getMessageLogContent(msg *schema.Message) string {
	body := getMessageLogBody(msg)
	if strings.TrimSpace(msg.ReasoningContent) == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Sprintf("reasoning:\n%s", msg.ReasoningContent)
	}
	return fmt.Sprintf("reasoning:\n%s\n\ncontent:\n%s", msg.ReasoningContent, body)
}

func getMessageLogBody(msg *schema.Message) string {
	if len(msg.ToolCalls) == 0 {
		return msg.Content
	}

	var sb strings.Builder
	for i, call := range msg.ToolCalls {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "tool: %s\narguments:\n%s", call.Function.Name, call.Function.Arguments)
	}
	return sb.String()
}

func markdownFence(content string) string {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	return fence
}

type transcriptMessage struct {
	Time    time.Time `json:"time"`
	Role    string    `json:"role"`
	Content string    `json:"content,omitempty"`
	Tools   []string  `json:"tools,omitempty"`
}

func appendTranscriptLog(ctx context.Context, dir string, messages []*schema.Message) {
	if dir == "" {
		return
	}
	sessionID := getTranscriptSessionID(ctx)
	if sessionID == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("transcript log: mkdir failed", "path", dir, "err", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, sessionID+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("transcript log: open failed", "path", dir, "err", err)
		return
	}
	defer f.Close()
	for _, msg := range messages {
		payload, err := json.Marshal(toTranscriptMessage(msg))
		if err != nil {
			slog.Warn("transcript log: marshal failed", "err", err)
			return
		}
		if _, err := f.Write(append(payload, '\n')); err != nil {
			slog.Warn("transcript log: write failed", "path", dir, "err", err)
			return
		}
	}
}

func toTranscriptMessage(msg *schema.Message) transcriptMessage {
	out := transcriptMessage{Time: time.Now().UTC()}
	if msg == nil {
		return out
	}
	out.Role = fmt.Sprint(msg.Role)
	if len(msg.ToolCalls) == 0 {
		out.Content = msg.Content
		return out
	}
	for _, call := range msg.ToolCalls {
		out.Tools = append(out.Tools, call.Function.Name)
	}
	return out
}

func getTranscriptSessionID(ctx context.Context) string {
	sessionID := runtimecontext.GetSessionID(ctx)
	if sessionID == "" {
		sessionID = consts.DefaultSessionID
	}
	return sanitizeTranscriptSessionID(sessionID)
}

func sanitizeTranscriptSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	var b strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}
