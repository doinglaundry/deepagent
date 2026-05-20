package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"eino-cli/backend/config"
)

type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func NewStoreFromConfig() *Store {
	return NewStore(config.MemoryDir())
}

func (s *Store) Load(agentName string) (MemoryData, error) {
	if err := validateAgentName(agentName); err != nil {
		return MemoryData{}, err
	}
	path := filepath.Join(s.dir, "global.json")
	if agentName != "" {
		path = filepath.Join(s.dir, "agents", agentName+".json")
	}
	payload, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return GetEmptyMemoryData(), nil
	}
	if err != nil {
		return MemoryData{}, fmt.Errorf("read memory: %w", err)
	}

	var data MemoryData
	err = json.Unmarshal(payload, &data)
	if err != nil {
		slog.Warn("memory store: malformed json, returning empty", "path", path, "err", err)
		return GetEmptyMemoryData(), nil
	}

	for i := range data.Facts {
		data.Facts[i].Confidence = CoerceConfidence(data.Facts[i].Confidence)
	}
	return data, nil
}

func (s *Store) Save(agentName string, data MemoryData) error {
	if err := validateAgentName(agentName); err != nil {
		return err
	}
	path := filepath.Join(s.dir, "global.json")
	if agentName != "" {
		path = filepath.Join(s.dir, "agents", agentName+".json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir memory dir: %w", err)
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("write tmp memory: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp memory: %w", err)
	}
	return nil
}
