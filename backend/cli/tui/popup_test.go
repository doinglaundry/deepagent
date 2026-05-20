package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
)

func newModelForPopup(value string) *Model {
	ti := textinput.New()
	ti.SetValue(value)
	return &Model{input: ti}
}

// Plain text without a leading slash must yield an empty popup so the
// menu stays out of the way of normal prompts.
func TestRenderPopup_HiddenWhenNoSlash(t *testing.T) {
	m := newModelForPopup("hello")
	if got := m.renderPopup(); got != "" {
		t.Errorf("popup must be hidden for non-slash input; got %q", got)
	}
}

// Once the user types a space, focus has moved into the argument
// region — the menu hides so /plan / /todos can take their
// on/off/open/close without competing with name suggestions.
func TestRenderPopup_HiddenWhenInArgRegion(t *testing.T) {
	m := newModelForPopup("/plan on")
	if got := m.renderPopup(); got != "" {
		t.Errorf("popup must hide once a space is typed; got %q", got)
	}
}

// Bare "/" is the discovery path — every command name should appear
// at least once so the user can browse without typing further.
func TestRenderPopup_ShowsAllOnBareSlash(t *testing.T) {
	m := newModelForPopup("/")
	out := m.renderPopup()
	for _, c := range commands {
		if !strings.Contains(out, "/"+c.Name) {
			t.Errorf("bare slash must list every command; missing /%s in %q",
				c.Name, out)
		}
	}
}

// Prefix filtering narrows the menu; "/pl" must surface /plan and
// drop unrelated entries like /clear.
func TestRenderPopup_PrefixFilters(t *testing.T) {
	m := newModelForPopup("/pl")
	out := m.renderPopup()
	if !strings.Contains(out, "/plan") {
		t.Errorf("/pl must surface /plan; got %q", out)
	}
	if strings.Contains(out, "/clear") {
		t.Errorf("/pl must filter out /clear; got %q", out)
	}
}

// Structural invariant: popupHeight is what recomputeLayout subtracts
// from the viewport budget. If renderPopup's actual line count drifts
// from popupHeight (e.g. somebody adds a header row to one but not the
// other) the input box silently leaves the screen. This test pins them
// together across the full state space — hidden / shown / overflow are
// all exercised through the cases.
func TestPopupHeight_MatchesRenderLineCount(t *testing.T) {
	cases := []string{"/", "/pl", "/zzz", "hello", "/plan on", ""}
	for _, value := range cases {
		m := newModelForPopup(value)
		out := m.renderPopup()
		want := 0
		if out != "" {
			want = strings.Count(out, "\n") + 1
		}
		if got := m.popupHeight(); got != want {
			t.Errorf("popupHeight=%d but renderPopup has %d lines for input %q",
				got, want, value)
		}
	}
}
