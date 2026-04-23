package retrieval

import (
	"strings"

	memorystore "eino-cli/internal/memory/store"
)

type Retriever struct {
	store *memorystore.Store
}

func New(store *memorystore.Store) *Retriever {
	return &Retriever{store: store}
}

func (r *Retriever) Find(query string) ([]memorystore.Memory, error) {
	memories, err := r.store.LoadAll()
	if err != nil {
		return nil, err
	}
	if query == "" {
		return memories, nil
	}
	matched := make([]memorystore.Memory, 0, len(memories))
	needle := strings.ToLower(query)
	for _, memory := range memories {
		if strings.Contains(strings.ToLower(memory.Key), needle) || strings.Contains(strings.ToLower(memory.Content), needle) {
			matched = append(matched, memory)
		}
	}
	return matched, nil
}
