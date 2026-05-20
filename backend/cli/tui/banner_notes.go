package tui

// releaseNotes is the right-column "What's new" content of the welcome
// card, newest first. Bump bannerVersion in the same commit you prepend a
// new entry — drift between the version number on the box border and the
// notes shown inside is a code-review red flag.
//
// Keep entries ASCII so plain runewidth math (1 cell per rune) stays
// accurate when the right column word-wraps; Chinese / emoji content goes
// into a spec doc instead.
var releaseNotes = []string{
	"yaml/config.yaml now only carries models and web_search",
	"rebuilt fs/shell tools; tool-call slog.Debug observability hook",
}
