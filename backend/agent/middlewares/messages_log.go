package middlewares

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type MessagesLog struct {
	*adk.BaseChatModelAgentMiddleware
	path string
	seen int
}

func NewMessagesLog(path string) *MessagesLog {
	return &MessagesLog{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		path:                         path,
	}
}

func (m *MessagesLog) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
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
		content = msg.Content
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

func markdownFence(content string) string {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	return fence
}
