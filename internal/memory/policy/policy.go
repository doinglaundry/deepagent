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
	return memory.Key != "" && utf8.RuneCountInString(strings.TrimSpace(memory.Content)) >= minContentLen
}
