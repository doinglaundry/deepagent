package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestBoxTitleLine_FixedWidthAndShape(t *testing.T) {
	got := boxTitleLine(40, "eino-cli v1.1.0")
	if w := runewidth.StringWidth(got); w != 40 {
		t.Errorf("width = %d, want 40", w)
	}
	if !strings.HasPrefix(got, "╭───") {
		t.Errorf("missing left dash run: %q", got)
	}
	if !strings.HasSuffix(got, "╮") {
		t.Errorf("missing right corner: %q", got)
	}
	if !strings.Contains(got, "eino-cli v1.1.0") {
		t.Errorf("title missing: %q", got)
	}
}

func TestBoxTitleLine_TruncatesOversizedTitle(t *testing.T) {
	got := boxTitleLine(14, "eino-cli v1.1.0")
	if w := runewidth.StringWidth(got); w != 14 {
		t.Errorf("width = %d, want 14", w)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis on truncation, got %q", got)
	}
}

func TestBoxTitleLine_DegenerateWidthFallsBackToPlainBorder(t *testing.T) {
	got := boxTitleLine(6, "anything")
	if w := runewidth.StringWidth(got); w != 6 {
		t.Errorf("width = %d, want 6", w)
	}
	if strings.Contains(got, "anything") {
		t.Errorf("title should be omitted at this width, got %q", got)
	}
}

func TestSplitColumns_PadsShorterAndKeepsTotalWidth(t *testing.T) {
	left := []string{"abc", "de"}
	right := []string{"x", "yy", "zzz"}
	out := splitColumns(left, right, 5, 4)
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	want0 := "│ abc   │ x    │"
	if out[0] != want0 {
		t.Errorf("row 0 = %q, want %q", out[0], want0)
	}
	for i, row := range out {
		if w := runewidth.StringWidth(row); w != 5+4+7 {
			t.Errorf("row %d width = %d, want %d", i, w, 5+4+7)
		}
	}
}

func TestGetRow_PadsTruncatesAndHandlesPastEnd(t *testing.T) {
	rows := []string{"hello"}
	if got := getRow(rows, 0, 10); got != "hello     " {
		t.Errorf("pad: got %q", got)
	}
	if got := getRow(rows, 0, 3); runewidth.StringWidth(got) != 3 || !strings.Contains(got, "…") {
		t.Errorf("truncate: got %q (width=%d)", got, runewidth.StringWidth(got))
	}
	if got := getRow(rows, 5, 4); got != "    " {
		t.Errorf("past-end: got %q", got)
	}
}
