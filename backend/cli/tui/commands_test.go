package tui

import "testing"

// shouldShowPopup is the only gate between "popup hidden" and "popup
// candidate set is computed". Each row pins down one boundary of the
// rule so a future refactor of the predicate can't drift silently.
func TestShouldShowPopup(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"hello", false},
		{"/", true},
		{"/cl", true},
		{"/clear", true},
		{"/plan on", false},     // space → arg region
		{"hello /clear", false}, // slash not at column 0
		{"/clear\t", false},     // tab counts as whitespace too
	}
	for _, tc := range cases {
		if got := shouldShowPopup(tc.in); got != tc.want {
			t.Errorf("shouldShowPopup(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Case-insensitive prefix match: "/PL" must surface /plan only.
func TestFilterCommands_PrefixCaseInsensitive(t *testing.T) {
	got := filterCommands(commands, "/PL")
	if len(got) != 1 || got[0].Name != "plan" {
		t.Errorf("expected only [plan], got %#v", got)
	}
}

// Bare "/" is the discovery path — must list every command so the user
// can browse without typing anything else.
func TestFilterCommands_EmptyReturnsAll(t *testing.T) {
	got := filterCommands(commands, "/")
	if len(got) != len(commands) {
		t.Errorf("/ should return all %d commands, got %d", len(commands), len(got))
	}
}

// Unknown prefix → empty slice. Callers gate popup display on len > 0,
// so this is what hides the menu when there's no useful suggestion.
func TestFilterCommands_NoMatchReturnsEmpty(t *testing.T) {
	got := filterCommands(commands, "/zzz")
	if len(got) != 0 {
		t.Errorf("/zzz should return empty, got %#v", got)
	}
}
