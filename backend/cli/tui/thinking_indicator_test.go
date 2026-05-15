package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
)

// streaming + populated verbs / elapsed → indicator carries the present-
// tense verb with ellipsis and the elapsed seconds with the "thinking" tag.
func TestRenderStreamPanel_DuringStreamingContainsVerbAndElapsed(t *testing.T) {
	m := &Model{
		streaming:   true,
		verbPresent: "Moonwalking",
		elapsed:     6 * time.Second,
	}
	got := m.renderStreamPanel()
	for _, want := range []string{"✶", "Moonwalking…", "6s", "thinking"} {
		if !strings.Contains(got, want) {
			t.Errorf("indicator missing %q; got: %q", want, got)
		}
	}
}

// Idle (not streaming, no error) → panel must be empty so the layout
// frees the line for viewport / todo panel use.
func TestRenderStreamPanel_IdleEmpty(t *testing.T) {
	m := &Model{streaming: false}
	if got := m.renderStreamPanel(); got != "" {
		t.Errorf("idle panel must be empty; got: %q", got)
	}
}

// Error sticks around as a one-line panel after a failed run; it must
// not be eclipsed by the thinking indicator (which only renders when
// streaming).
func TestRenderStreamPanel_ErrorRendersWhenNotStreaming(t *testing.T) {
	m := &Model{streaming: false, lastErr: errFixed("rate limited")}
	got := m.renderStreamPanel()
	if !strings.Contains(got, "rate limited") {
		t.Errorf("expected error body in panel; got %q", got)
	}
}

// spinner.TickMsg drives elapsed seconds while streaming. We can't drive
// real time forward in a test, but we can set streamStart far in the
// past and verify the next tick updates elapsed to ≥ that gap.
func TestSpinnerTick_UpdatesElapsedWhileStreaming(t *testing.T) {
	m := &Model{
		streaming:   true,
		streamStart: time.Now().Add(-3 * time.Second),
		spin:        spinner.New(),
	}
	_, _ = m.Update(m.spin.Tick())
	if m.elapsed < 3*time.Second {
		t.Errorf("elapsed must reflect time since streamStart; got %v", m.elapsed)
	}
}

// Not streaming → spinner tick must NOT update elapsed (panel is hidden,
// computing elapsed would be wasted work and shows a stale value if the
// next turn starts).
func TestSpinnerTick_IgnoredWhenNotStreaming(t *testing.T) {
	m := &Model{
		streaming:   false,
		streamStart: time.Now().Add(-3 * time.Second),
		elapsed:     0,
		spin:        spinner.New(),
	}
	_, _ = m.Update(m.spin.Tick())
	if m.elapsed != 0 {
		t.Errorf("elapsed must stay zero when idle; got %v", m.elapsed)
	}
}

// errFixed is a minimal error helper to avoid pulling errors.New in tests.
type errFixed string

func (e errFixed) Error() string { return string(e) }
