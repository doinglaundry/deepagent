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
	flushed   bool
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
	fmt.Fprintf(&sb, "%s %s\n", systemPrefixStyle.Render("•"), toolHeaderStyle.Render(formatToolTitle(block)))

	lines := block.lines
	collapsed := block.collapsed && len(lines) > previewLines
	if collapsed {
		lines = lines[:previewLines]
	}
	for i, line := range lines {
		prefix := "     "
		if i == 0 {
			prefix = "  ⎿  "
		}
		sb.WriteString(toolBodyStyle.Render(prefix + line))
		sb.WriteByte('\n')
	}
	if collapsed {
		fmt.Fprintf(&sb, "%s", toolFooterStyle.Render(fmt.Sprintf("     … +%d lines (ctrl+o to expand)", len(block.lines)-previewLines)))
		return sb.String()
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatToolTitle(block *toolBlock) string {
	if block == nil {
		return ""
	}
	return fmt.Sprintf("Ran %s(%s)", block.name, block.argsLine)
}

func renderExpandedToolBlockCopy(block *toolBlock) string {
	if block == nil {
		return ""
	}
	wasCollapsed := block.collapsed
	block.collapsed = false
	out := renderToolBlock(block, len(block.lines))
	block.collapsed = wasCollapsed
	return out
}

func formatArgsLine(name, rawArgs string, max int) string {
	switch name {
	case "execute", "bash", "Bash":
		if command, ok := extractShellCommand(rawArgs); ok {
			return truncateRunes(command, max)
		}
	case "read_file", "ls", "glob", "grep", "Read", "Glob", "Grep":
		if summary, ok := extractPathSearchArgs(rawArgs); ok {
			return truncateRunes(summary, max)
		}
	case "write_file", "Write", "edit", "Edit", "edit_file", "str_replace", "StrReplace":
		if path, bodyLen, ok := extractFileWriteArgs(rawArgs); ok {
			return fmt.Sprintf("%s, %d bytes", path, bodyLen)
		}
	case "ask_clarification":
		if question, ok := extractClarificationArgs(rawArgs); ok {
			return truncateRunes(question, max)
		}
	}
	return truncateRunes(rawArgs, max)
}

func extractShellCommand(rawArgs string) (string, bool) {
	var value struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &value); err != nil {
		return "", false
	}
	if value.Description != "" && value.Command != "" {
		return value.Description + ": " + value.Command, true
	}
	if value.Command != "" {
		return value.Command, true
	}
	return "", false
}

func extractPathSearchArgs(rawArgs string) (string, bool) {
	var value struct {
		Path       string `json:"path"`
		FilePath   string `json:"file_path"`
		Pattern    string `json:"pattern"`
		OutputMode string `json:"output_mode"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &value); err != nil {
		return "", false
	}
	path := value.Path
	if path == "" {
		path = value.FilePath
	}
	var parts []string
	if path != "" {
		parts = append(parts, path)
	}
	if value.Pattern != "" {
		parts = append(parts, "pattern="+value.Pattern)
	}
	if value.OutputMode != "" {
		parts = append(parts, "mode="+value.OutputMode)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, ", "), true
}

func extractClarificationArgs(rawArgs string) (string, bool) {
	var value struct {
		Question string `json:"question"`
		Prompt   string `json:"prompt"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &value); err != nil {
		return "", false
	}
	for _, candidate := range []string{value.Question, value.Prompt, value.Message} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate), true
		}
	}
	return "", false
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

func getToolBlockByID(m *Model, id int) *toolBlock {
	for _, block := range m.toolBlocks {
		if block.id == id {
			return block
		}
	}
	return nil
}

func getLatestCollapsibleToolBlock(m *Model) *toolBlock {
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
