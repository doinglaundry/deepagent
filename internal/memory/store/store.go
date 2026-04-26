package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TaskMemoryPrefix is the content prefix used by task records written to the
// memory store. Consumers can use this to exclude task entries from context.
const TaskMemoryPrefix = "task "

type Memory struct {
	Key         string    `json:"key"`
	Content     string    `json:"content"`
	Scope       string    `json:"scope"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Store struct {
	dir string
}

func New(content string) Memory {
	return Memory{Content: content}
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Save(memory Memory) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}
	payload, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, memory.Key+".json"), payload, 0o644); err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	return nil
}

func (s *Store) LoadAll() ([]Memory, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memory directory: %w", err)
	}

	memories := make([]Memory, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read memory: %w", err)
		}
		var memory Memory
		if err := json.Unmarshal(payload, &memory); err != nil {
			return nil, fmt.Errorf("unmarshal memory: %w", err)
		}
		memories = append(memories, memory)
	}
	return memories, nil
}
