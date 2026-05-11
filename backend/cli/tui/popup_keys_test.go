package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func newModelForPopupKeys(value string, sel int) *Model {
	ti := textinput.New()
	ti.SetValue(value)
	return &Model{input: ti, popupSel: sel}
}

// Down advances selection by one. At "/" the candidate set is the full
// command list; sel 0 → 1 lands on the second entry.
func TestHandlePopupKey_DownArrowAdvancesSelection(t *testing.T) {
	m := newModelForPopupKeys("/", 0)
	_, handled := m.handlePopupKey(tea.KeyMsg{Type: tea.KeyDown})
	if !handled {
		t.Fatal("Down must be consumed when popup is open")
	}
	if m.popupSel != 1 {
		t.Errorf("popupSel after Down = %d, want 1", m.popupSel)
	}
}

// Up from the top wraps to the bottom — circular navigation is the
// least-surprise default for short menus.
func TestHandlePopupKey_UpArrowWraps(t *testing.T) {
	m := newModelForPopupKeys("/", 0)
	m.handlePopupKey(tea.KeyMsg{Type: tea.KeyUp})
	want := len(commands) - 1
	if m.popupSel != want {
		t.Errorf("popupSel after Up from 0 = %d, want %d (wrap to last)",
			m.popupSel, want)
	}
}

// Tab accepts the selected entry by rewriting input to "/<name>". For
// "/de" only /debug matches → sel=0 → input becomes "/debug".
func TestHandlePopupKey_TabAcceptsSelectedCommand(t *testing.T) {
	m := newModelForPopupKeys("/de", 0)
	_, handled := m.handlePopupKey(tea.KeyMsg{Type: tea.KeyTab})
	if !handled {
		t.Fatal("Tab must be consumed when popup is open")
	}
	if got := m.input.Value(); got != "/debug" {
		t.Errorf("Tab must rewrite input to /debug; got %q", got)
	}
}

// Enter rewrites input then declines to handle the key — the outer
// KeyEnter runs submit() on the now-complete value, going through the
// same dispatch path as a manually-typed command. handlePopupKey owns
// the rewrite; the outer code owns the submission.
func TestHandlePopupKey_EnterFallsThroughForSubmit(t *testing.T) {
	m := newModelForPopupKeys("/cl", 0)
	cmd, handled := m.handlePopupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if handled {
		t.Error("Enter must return handled=false so outer KeyEnter submits")
	}
	if cmd != nil {
		t.Error("Enter must not return a tea.Cmd from the popup branch")
	}
	if m.input.Value() != "/clear" {
		t.Errorf("Enter must accept selected command first; input=%q",
			m.input.Value())
	}
}

// Esc collapses the popup by emptying input. The outer ESC chain
// (abort streaming, clear input) only runs on a subsequent Esc, when
// the popup is no longer claiming the key.
func TestHandlePopupKey_EscClosesPopupByEmptyingInput(t *testing.T) {
	m := newModelForPopupKeys("/de", 0)
	_, handled := m.handlePopupKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled {
		t.Fatal("Esc must be consumed when popup is open")
	}
	if m.input.Value() != "" {
		t.Errorf("Esc must clear input (and thus hide popup); got %q",
			m.input.Value())
	}
}

// When the match set shrinks below the current sel, sel must reset to
// 0 — otherwise renderPopup would highlight nothing (or, worse, the
// renderer's defensive clamp would silently mask a logic bug).
func TestOnInputChanged_ResetsSelOnRangeShrink(t *testing.T) {
	ti := textinput.New()
	ti.SetValue("/")
	m := &Model{input: ti, popupSel: 5} // sel=5 valid for the full pool

	m.input.SetValue("/de")
	m.onInputChanged()

	if m.popupSel != 0 {
		t.Errorf("popupSel must reset to 0 when matches shrink below it; got %d",
			m.popupSel)
	}
}
