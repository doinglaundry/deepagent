package policy

import memorystore "eino-cli/internal/memory/store"

type Policy struct{}

func New() *Policy {
	return &Policy{}
}

func (p *Policy) Allow(memory memorystore.Memory) bool {
	return memory.Key != "" && memory.Content != ""
}
