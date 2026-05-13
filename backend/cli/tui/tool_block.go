package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/schema"
)

const toolPlaceholderPrefix = "[tool:#"

type toolBlock struct {
	id        int
	name      string
	argsLine  string
	lines     []string
	collapsed bool
}

func extractNewToolBlocks(messages []*schema.Message, prevCount int, idSeq *int, argsMax int) []*toolBlock {
	if prevCount < 0 {
		prevCount = 0
	}
	if prevCount >= len(messages) {
		return nil
	}

	callsByID := make(map[string]schema.ToolCall)
	for _, msg := range messages[prevCount:] {
		if msg == nil || msg.Role != schema.Assistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				callsByID[call.ID] = call
			}
		}
	}

	var blocks []*toolBlock
	for _, msg := range messages[prevCount:] {
		if msg == nil || msg.Role != schema.Tool || msg.ToolCallID == "" {
			continue
		}
		call, ok := callsByID[msg.ToolCallID]
		if !ok {
			continue
		}
		lines := splitToolLines(msg.Content)
		if len(lines) == 0 {
			continue
		}
		*idSeq = *idSeq + 1
		blocks = append(blocks, &toolBlock{
			id:        *idSeq,
			name:      call.Function.Name,
			argsLine:  formatArgsLine(call.Function.Name, call.Function.Arguments, argsMax),
			lines:     lines,
			collapsed: true,
		})
	}
	return blocks
}

func renderToolBlock(block *toolBlock, previewLines int) string {
	if block == nil {
		return ""
	}
	if previewLines <= 0 {
		previewLines = defaultToolPreviewLines
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s(%s)\n", toolHeaderStyle.Render("⏺"), block.name, block.argsLine)

	lines := block.lines
	collapsed := block.collapsed && len(lines) > previewLines
	if collapsed {
		lines = lines[:previewLines]
	}
	for i, line := range lines {
		sb.WriteString(toolBodyStyle.Render(toolBodyPrefix(i == 0) + line))
		sb.WriteByte('\n')
	}
	if collapsed {
		fmt.Fprintf(&sb, "%s", toolFooterStyle.Render(fmt.Sprintf("     … +%d lines (ctrl+o to expand)", len(block.lines)-previewLines)))
		return sb.String()
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatArgsLine(name, rawArgs string, max int) string {
	switch name {
	case "write_file", "Write", "edit", "Edit", "str_replace", "StrReplace":
		if path, bodyLen, ok := extractFileWriteArgs(rawArgs); ok {
			return fmt.Sprintf("%s, %d bytes", path, bodyLen)
		}
	}
	return truncateRunes(rawArgs, max)
}

func extractFileWriteArgs(rawArgs string) (path string, bodyLen int, ok bool) {
	var value struct {
		Path      string `json:"path"`
		FilePath  string `json:"file_path"`
		Content   string `json:"content"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &value); err != nil {
		return "", 0, false
	}
	path = value.Path
	if path == "" {
		path = value.FilePath
	}
	body := value.Content
	if body == "" {
		body = value.NewString
	}
	if path == "" {
		return "", 0, false
	}
	return path, len(body), true
}

func splitToolLines(content string) []string {
	content = strings.TrimRight(content, "\n")
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func toolBodyPrefix(first bool) string {
	if first {
		return "  ⎿  "
	}
	return "     "
}

func toolPlaceholder(id int) string {
	return fmt.Sprintf("%s%d]", toolPlaceholderPrefix, id)
}

func parseToolPlaceholder(content string) (int, bool) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, toolPlaceholderPrefix) || !strings.HasSuffix(content, "]") {
		return 0, false
	}
	id, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(content, toolPlaceholderPrefix), "]"))
	if err != nil {
		return 0, false
	}
	return id, true
}

func (m *Model) findToolBlockByID(id int) *toolBlock {
	for _, block := range m.toolBlocks {
		if block.id == id {
			return block
		}
	}
	return nil
}

func (m *Model) latestCollapsibleToolBlock() *toolBlock {
	previewLines := m.toolPreviewLines
	if previewLines <= 0 {
		previewLines = defaultToolPreviewLines
	}
	for i := len(m.toolBlocks) - 1; i >= 0; i-- {
		if len(m.toolBlocks[i].lines) > previewLines {
			return m.toolBlocks[i]
		}
	}
	return nil
}

type footerHintExpiredMsg struct{}

func expireFooterHint(after time.Duration) tea.Cmd {
	return tea.Tick(after, func(time.Time) tea.Msg {
		return footerHintExpiredMsg{}
	})
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
