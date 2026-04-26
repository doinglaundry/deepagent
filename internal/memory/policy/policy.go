package policy

import (
	"strings"
	"unicode/utf8"

	memorystore "eino-cli/internal/memory/store"
)

const minContentLen = 8

type Policy struct{}

func New() *Policy {
	return &Policy{}
}

func (p *Policy) Allow(memory memorystore.Memory) bool {
	if memory.Key == "" {
		return false
	}
	trimmed := strings.TrimSpace(memory.Content)
	// Byte length >= minContentLen is a sufficient check for ASCII (common case).
	// Only count runes for multibyte content where byte count could overcount.
	return len(trimmed) >= minContentLen && utf8.RuneCountInString(trimmed) >= minContentLen
}
