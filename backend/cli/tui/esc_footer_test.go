package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// ESC during streaming → abortStream fires (cancel called), model stays
// alive. Without this binding users had to dig for Ctrl-C while watching
// the indicator spin.
func TestHandleKey_EscDuringStreamAborts(t *testing.T) {
	cancelled := false
	m := &Model{
		streaming: true,
		cancel:    func() { cancelled = true },
	}
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})

	if !cancelled {
		t.Errorf("ESC during streaming must call cancel")
	}
	if cmd != nil {
		t.Errorf("ESC abort must not return a follow-up cmd; got %v", cmd)
	}
}

// ESC while idle → input gets cleared, no quit. Mirrors Ctrl-U semantics
// for users who started typing the wrong thing.
func TestHandleKey_EscIdleClearsInput(t *testing.T) {
	ti := textinput.New()
	ti.SetValue("half-typed garbage")
	m := &Model{streaming: false, input: ti}

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})

	if m.input.Value() != "" {
		t.Errorf("ESC while idle must clear input; got %q", m.input.Value())
	}
}

// Streaming footer must surface the only useful binding: ESC to bail.
func TestRenderFooter_StreamingShowsInterrupt(t *testing.T) {
	m := &Model{streaming: true, width: 80, modelName: "kimi"}
	got := m.renderFooter()
	if !strings.Contains(got, "esc to interrupt") {
		t.Errorf("streaming footer missing 'esc to interrupt'; got %q", got)
	}
}

// Idle footer must point at the discovery path (/help) and exit binding.
func TestRenderFooter_IdleShowsHelpHint(t *testing.T) {
	m := &Model{streaming: false, width: 80, modelName: "kimi"}
	got := m.renderFooter()
	for _, want := range []string{"/help", "ctrl-c"} {
		if !strings.Contains(got, want) {
			t.Errorf("idle footer missing %q; got %q", want, got)
		}
	}
	if strings.Contains(got, "esc to interrupt") {
		t.Errorf("idle footer must not show interrupt hint; got %q", got)
	}
}

// Token total > 0 → "<x>k tokens" segment appears next to modelName.
// Decimal kilo-format is the band-of-interest for typical LLM turns
// (1k–100k); below 1k falls back to plain integer count.
func TestRenderFooter_ShowsTokenTotalWhenPresent(t *testing.T) {
	m := &Model{streaming: false, width: 80, modelName: "kimi", tokenTotal: 3400}
	got := m.renderFooter()
	if !strings.Contains(got, "3.4k tokens") {
		t.Errorf("footer missing token total; got %q", got)
	}
}

// Token total = 0 → segment hidden so empty sessions stay quiet
// (zero-state noise was the v1 ask).
func TestRenderFooter_HidesTokenTotalAtZero(t *testing.T) {
	m := &Model{streaming: false, width: 80, modelName: "kimi", tokenTotal: 0}
	got := m.renderFooter()
	if strings.Contains(got, "tokens") {
		t.Errorf("zero-token footer must not include 'tokens'; got %q", got)
	}
}

// formatTokenCount: <1000 stays integer, ≥1000 becomes decimal kilo.
// Tight test — single string in/out — locks the format string so the
// footer stays readable at narrow widths.
func TestFormatTokenCount(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 tokens"},
		{42, "42 tokens"},
		{1000, "1.0k tokens"},
		{3400, "3.4k tokens"},
		{12500, "12.5k tokens"},
	}
	for _, c := range cases {
		if got := formatTokenCount(c.in); got != c.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
