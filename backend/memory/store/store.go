// Package store persists per-agent rich memory snapshots as JSON files under
// {RootDir}/.eino-cli/memory/. Files are written atomically (tmp + rename)
// and the schema (MemoryData) is 1:1 with deer-flow on disk.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"eino-cli/backend/config"
)

// memorySubdir is appended to cfg.RootDir to derive the on-disk memory root.
// Kept private; callers go through NewStoreFromConfig.
const memorySubdir = ".eino-cli/memory"

// Store is a thin wrapper around a directory; intentionally has no cache
// (CLI is single-process, short-lived; correctness > one fewer disk read).
type Store struct {
	dir string
}

// NewStore is the test-friendly constructor; production code should prefer
// NewStoreFromConfig so all stores share the same dir derivation.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// NewStoreFromConfig derives the memory dir from cfg.RootDir. Multiple calls
// are equivalent because Store is stateless.
func NewStoreFromConfig(cfg *config.Config) *Store {
	return NewStore(filepath.Join(cfg.RootDir, memorySubdir))
}

// Load returns the MemoryData for agentName ("" == global). Missing file or
// malformed JSON both yield an empty MemoryData with no error: the caller
// (renderer / updater) treats "no memory" and "broken memory" identically,
// and a future Save will overwrite the broken file cleanly.
func (s *Store) Load(agentName string) (MemoryData, error) {
	path, err := s.getPath(agentName)
	if err != nil {
		return MemoryData{}, err
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

// Save writes data for agentName atomically: tmp file in the same dir, then
// rename. POSIX rename(2) is atomic so a crash between WriteFile and Rename
// either leaves the previous file intact or the new one in full.
func (s *Store) Save(agentName string, data MemoryData) error {
	path, err := s.getPath(agentName)
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		return fmt.Errorf("mkdir memory dir: %w", err)
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memory: %w", err)
	}

	// Random 8-hex suffix so concurrent saves on the same agent don't stomp
	// each other's tmp file before rename.
	var nonce [4]byte
	_, err = rand.Read(nonce[:])
	if err != nil {
		return fmt.Errorf("rand nonce: %w", err)
	}
	tmp := fmt.Sprintf("%s.%s.tmp", path, hex.EncodeToString(nonce[:]))

	err = os.WriteFile(tmp, payload, 0o644)
	if err != nil {
		return fmt.Errorf("write tmp memory: %w", err)
	}

	err = os.Rename(tmp, path)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp memory: %w", err)
	}
	return nil
}

// getPath maps agentName -> file path; "" goes to global.json, else
// agents/<name>.json. Validates name first to refuse path traversal.
func (s *Store) getPath(agentName string) (string, error) {
	err := validateAgentName(agentName)
	if err != nil {
		return "", err
	}
	if agentName == "" {
		return filepath.Join(s.dir, "global.json"), nil
	}
	return filepath.Join(s.dir, "agents", agentName+".json"), nil
}
