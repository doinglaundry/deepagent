package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent/middlewares"
)

func TestExtractToolBlocksSingleCall(t *testing.T) {
	idSeq := 0
	messages := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call-1", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"git log"}`}},
			},
		},
		{Role: schema.Tool, ToolCallID: "call-1", Content: "one\ntwo"},
	}

	blocks := extractNewToolBlocks(messages, 0, &idSeq, 60)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	got := blocks[0]
	if got.id != 1 || got.name != "Bash" || got.argsLine != "git log" {
		t.Fatalf("unexpected block: %#v", got)
	}
	if strings.Join(got.lines, ",") != "one,two" {
		t.Fatalf("unexpected lines: %#v", got.lines)
	}
}

func TestExtractToolBlocksMultipleInOneTurn(t *testing.T) {
	idSeq := 0
	messages := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "a", Function: schema.FunctionCall{Name: "Read", Arguments: `{"path":"a"}`}},
				{ID: "b", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"pwd"}`}},
			},
		},
		{Role: schema.Tool, ToolCallID: "a", Content: "file"},
		{Role: schema.Tool, ToolCallID: "b", Content: "pwd"},
	}

	blocks := extractNewToolBlocks(messages, 0, &idSeq, 60)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].name != "Read" || blocks[1].name != "Bash" {
		t.Fatalf("order not preserved: %#v", blocks)
	}
}

func TestExtractToolBlocksDanglingCallDropped(t *testing.T) {
	idSeq := 0
	messages := []*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{ID: "call-1", Function: schema.FunctionCall{Name: "Bash", Arguments: `{}`}},
		},
	}}

	if blocks := extractNewToolBlocks(messages, 0, &idSeq, 60); len(blocks) != 0 {
		t.Fatalf("dangling call should be dropped: %#v", blocks)
	}
}

func TestExtractToolBlocksPrevCountRespected(t *testing.T) {
	idSeq := 0
	messages := []*schema.Message{
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "old", Function: schema.FunctionCall{Name: "Bash"}}}},
		{Role: schema.Tool, ToolCallID: "old", Content: "old"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "new", Function: schema.FunctionCall{Name: "Read"}}}},
		{Role: schema.Tool, ToolCallID: "new", Content: "new"},
	}

	blocks := extractNewToolBlocks(messages, 2, &idSeq, 60)
	if len(blocks) != 1 || blocks[0].name != "Read" {
		t.Fatalf("expected only new block, got %#v", blocks)
	}
}

func TestFormatArgsLineWriteFile(t *testing.T) {
	got := formatArgsLine("write_file", `{"path":"a.md","content":"hello"}`, 60)
	if got != "a.md, 5 bytes" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatArgsLineEditNewString(t *testing.T) {
	got := formatArgsLine("Edit", `{"file_path":"a.go","new_string":"abc"}`, 60)
	if got != "a.go, 3 bytes" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatArgsLineUnknownToolFallback(t *testing.T) {
	got := formatArgsLine("Unknown", "1234567890", 4)
	if got != "1234…" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderToolBlockCollapsedFooter(t *testing.T) {
	block := &toolBlock{
		name:      "Bash",
		argsLine:  "git log --oneline -30",
		lines:     []string{"1", "2", "3", "4", "5", "6", "7"},
		collapsed: true,
	}

	got := renderToolBlock(block, 5)
	for _, want := range []string{"⏺", "Bash(git log --oneline -30)", "  ⎿  1", "… +2 lines (ctrl+o to expand)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered block missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "     6") {
		t.Fatalf("collapsed block should hide line 6:\n%s", got)
	}
}

func TestRenderToolBlockExpanded(t *testing.T) {
	block := &toolBlock{
		name:      "Bash",
		argsLine:  "pwd",
		lines:     []string{"1", "2", "3"},
		collapsed: false,
	}

	got := renderToolBlock(block, 1)
	if !strings.Contains(got, "     3") || strings.Contains(got, "ctrl+o") {
		t.Fatalf("expanded render wrong:\n%s", got)
	}
}

func TestRenderToolBlockShortNoFooter(t *testing.T) {
	block := &toolBlock{
		name:      "Bash",
		argsLine:  "pwd",
		lines:     []string{"1", "2", "3"},
		collapsed: true,
	}

	got := renderToolBlock(block, 5)
	if strings.Contains(got, "ctrl+o") {
		t.Fatalf("short block should not show footer:\n%s", got)
	}
}

func TestLatestCollapsibleToolBlockPicksLastLong(t *testing.T) {
	m := &Model{
		toolPreviewLines: 2,
		toolBlocks: []*toolBlock{
			{lines: []string{"1", "2", "3"}},
			{lines: []string{"1"}},
			{lines: []string{"1", "2", "3", "4"}},
		},
	}

	if got := m.latestCollapsibleToolBlock(); got != m.toolBlocks[2] {
		t.Fatalf("expected latest long block, got %#v", got)
	}
}

func TestLatestCollapsibleToolBlockNoneCollapsible(t *testing.T) {
	m := &Model{
		toolPreviewLines: 5,
		toolBlocks:       []*toolBlock{{lines: []string{"1"}}, {lines: []string{"1", "2"}}},
	}

	if got := m.latestCollapsibleToolBlock(); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestHandleKeyCtrlOToggles(t *testing.T) {
	m := &Model{
		toolPreviewLines: 1,
		toolBlocks:       []*toolBlock{{id: 1, lines: []string{"1", "2"}, collapsed: true}},
		viewport:         viewport.New(80, 10),
	}

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.toolBlocks[0].collapsed {
		t.Fatal("Ctrl-O should expand latest block")
	}
}

func TestHandleKeyCtrlONoBlocksSetsHint(t *testing.T) {
	m := &Model{toolPreviewLines: 1, viewport: viewport.New(80, 10)}

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.footerHint != "nothing to expand" {
		t.Fatalf("unexpected footer hint %q", m.footerHint)
	}
	if cmd == nil {
		t.Fatal("expected expiry command")
	}
}

func TestFooterHintExpires(t *testing.T) {
	m := &Model{footerHint: "nothing to expand"}
	_, _ = m.Update(footerHintExpiredMsg{})
	if m.footerHint != "" {
		t.Fatalf("footer hint should expire, got %q", m.footerHint)
	}
}

func TestToolBlockSettingsDefault(t *testing.T) {
	enabled, previewLines, argsMaxChars := getToolBlockSettings()
	if !enabled || previewLines != 5 || argsMaxChars != 60 {
		t.Fatalf("unexpected defaults: %v %d %d", enabled, previewLines, argsMaxChars)
	}
}

func TestHandleTraceEventExtractsAndPushesBlock(t *testing.T) {
	m := &Model{
		toolBlocksEnabled: true,
		toolPreviewLines:  defaultToolPreviewLines,
		toolArgsMaxChars:  defaultToolArgsMaxChars,
		viewport:          viewport.New(80, 10),
		width:             80,
		height:            24,
	}
	ev := middlewares.TraceEvent{
		Phase: middlewares.TracePhaseBefore,
		Messages: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{ID: "call-1", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"pwd"}`}},
				},
			},
			{Role: schema.Tool, ToolCallID: "call-1", Content: "ok"},
		},
	}

	_, _ = m.handleTraceEvent(ev)
	if len(m.toolBlocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(m.toolBlocks))
	}
	if len(m.messages) != 1 || m.messages[0].Content != "[tool:#1]" {
		t.Fatalf("unexpected messages: %#v", m.messages)
	}
	if m.lastSeenMsgCount != len(ev.Messages) {
		t.Fatalf("lastSeenMsgCount not updated: %d", m.lastSeenMsgCount)
	}
}

func TestToolBlockStaysLiveBeforeFinalAssistant(t *testing.T) {
	m := &Model{
		toolPreviewLines: defaultToolPreviewLines,
		viewport:         viewport.New(80, 10),
		width:            80,
		height:           24,
		toolBlocks: []*toolBlock{{
			id:       1,
			name:     "web_search",
			argsLine: `{"query":"x"}`,
			lines:    []string{"result"},
		}},
		messages: []chatMessage{
			{Role: "user", Content: "question"},
			{Role: "tool-block", Content: "[tool:#1]"},
		},
		flushedMsgCount: 1,
	}

	m.pushMessage("assistant", "answer")
	live := m.liveMessages()
	if len(live) != 2 || live[0].Role != "tool-block" || live[1].Role != "assistant" {
		t.Fatalf("tool block must stay live before final assistant, got %#v", live)
	}
	if len(m.pendingScrollback) != 0 {
		t.Fatalf("tool block should not be flushed before assistant renders, got %d pending", len(m.pendingScrollback))
	}
}

func TestDoneDrainsQueuedToolTraceBeforeAssistant(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	ch <- middlewares.TraceEvent{
		Phase: middlewares.TracePhaseBefore,
		Messages: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{ID: "call-1", Function: schema.FunctionCall{Name: "web_search", Arguments: `{"query":"x"}`}},
				},
			},
			{Role: schema.Tool, ToolCallID: "call-1", Content: "result"},
		},
	}
	close(ch)

	m := &Model{
		streaming:         true,
		streamCh:          ch,
		toolBlocksEnabled: true,
		toolPreviewLines:  defaultToolPreviewLines,
		toolArgsMaxChars:  defaultToolArgsMaxChars,
		viewport:          viewport.New(80, 10),
		width:             80,
		height:            24,
		messages:          []chatMessage{{Role: "user", Content: "question"}},
		flushedMsgCount:   1,
	}

	_, _ = m.handleDone(doneMsg{output: "answer"})
	live := m.liveMessages()
	if len(live) != 0 {
		t.Fatalf("done must flush completed turn out of live viewport, got %#v", live)
	}
	if len(m.pendingScrollback) != 2 {
		t.Fatalf("done must queue tool trace and answer for scrollback, got %d pending", len(m.pendingScrollback))
	}
	if !strings.Contains(m.pendingScrollback[0], "web_search") || !strings.Contains(m.pendingScrollback[1], "answer") {
		t.Fatalf("scrollback order must be tool trace before answer, got %#v", m.pendingScrollback)
	}
}

func TestLateToolTraceInsertsBeforeTrailingAssistant(t *testing.T) {
	m := &Model{
		toolBlocksEnabled: true,
		toolPreviewLines:  defaultToolPreviewLines,
		toolArgsMaxChars:  defaultToolArgsMaxChars,
		viewport:          viewport.New(80, 10),
		width:             80,
		height:            24,
		messages: []chatMessage{
			{Role: "user", Content: "question"},
			{Role: "assistant", Content: "answer"},
		},
		flushedMsgCount: 1,
	}

	_, _ = m.handleTraceEvent(middlewares.TraceEvent{
		Phase: middlewares.TracePhaseBefore,
		Messages: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{ID: "call-1", Function: schema.FunctionCall{Name: "web_search", Arguments: `{"query":"x"}`}},
				},
			},
			{Role: schema.Tool, ToolCallID: "call-1", Content: "result"},
		},
	})
	live := m.liveMessages()
	if len(live) != 2 || live[0].Role != "tool-block" || live[1].Role != "assistant" {
		t.Fatalf("late tool trace must be inserted before trailing assistant, got %#v", live)
	}
}

func TestClearResetsToolBlocks(t *testing.T) {
	m := &Model{
		rt:               stubRuntime{},
		width:            80,
		height:           24,
		modelName:        "stub-model",
		cwd:              ".",
		viewport:         viewport.New(80, 10),
		toolBlocks:       []*toolBlock{{id: 1, lines: []string{"x"}}},
		lastSeenMsgCount: 2,
		toolBlockSeq:     1,
		footerHint:       "nothing to expand",
	}

	_, handled := m.handleBuiltin("/clear")
	if !handled {
		t.Fatal("/clear should be handled")
	}
	if len(m.toolBlocks) != 0 || m.lastSeenMsgCount != 0 || m.toolBlockSeq != 0 || m.footerHint != "" {
		t.Fatalf("clear did not reset tool state: %#v", m)
	}
}
