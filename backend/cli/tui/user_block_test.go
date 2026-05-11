package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// userBlockStyle must have a background colour set — that's the "shadow"
// card effect distinguishing user input from the prefix-only assistant
// reply. A bare style here would silently undo the visual differentiation.
func TestUserBlockStyle_HasBackground(t *testing.T) {
	if _, isNone := userBlockStyle.GetBackground().(lipgloss.NoColor); isNone {
		t.Errorf("userBlockStyle.Background is unset; user echo would look identical to assistant")
	}
}

// User block padding gives the card breathing room around the text. Zero
// padding glues the background tight to the glyph and reads as a stray
// highlight rather than a card.
func TestUserBlockStyle_HasHorizontalPadding(t *testing.T) {
	if userBlockStyle.GetPaddingLeft() < 1 || userBlockStyle.GetPaddingRight() < 1 {
		t.Errorf("userBlockStyle must have left+right padding for the card look; got L=%d R=%d",
			userBlockStyle.GetPaddingLeft(), userBlockStyle.GetPaddingRight())
	}
}

// Rendered user line must still contain the original prompt content;
// the wrapping style mustn't drop characters or duplicate the prefix.
func TestRenderMessage_UserContainsPromptAndPrefix(t *testing.T) {
	m := &Model{}
	out := m.renderMessage(chatMessage{Role: "user", Content: "你好呀"})
	for _, want := range []string{"❯", "你好呀"} {
		if !strings.Contains(out, want) {
			t.Errorf("user render missing %q; got: %q", want, out)
		}
	}
}
