package policy

import (
	"strings"

	memorystore "eino-cli/internal/memory/store"
)

const minContentLen = 8

type Policy struct{}

func New() *Policy {
	return &Policy{}
}

func (p *Policy) Allow(memory memorystore.Memory) bool {
	return memory.Key != "" && len([]rune(strings.TrimSpace(memory.Content))) >= minContentLen
}
