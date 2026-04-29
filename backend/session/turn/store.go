package turn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"eino-cli/backend/session"
)

type Store struct {
	sessionsDir string
}

func NewStore(sessionsDir string) *Store {
	return &Store{sessionsDir: sessionsDir}
}

func (s *Store) turnsDir(sessionID string) string {
	return filepath.Join(s.sessionsDir, sessionID, "turns")
}

func (s *Store) turnPath(sessionID string, index int) string {
	return filepath.Join(s.turnsDir(sessionID), fmt.Sprintf("%04d.json", index))
}

func (s *Store) Save(t session.Turn) error {
	dir := s.turnsDir(t.SessionID)
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return fmt.Errorf("create turns directory: %w", err)
	}
	payload, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal turn: %w", err)
	}
	err = os.WriteFile(s.turnPath(t.SessionID, t.Index), payload, 0o644)
	if err != nil {
		return fmt.Errorf("write turn: %w", err)
	}
	return nil
}

func (s *Store) LoadAll(sessionID string) ([]session.Turn, error) {
	dir := s.turnsDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read turns directory: %w", err)
	}

	files := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		files = append(files, entry)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	turns := make([]session.Turn, 0, len(files))
	for _, f := range files {
		payload, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, fmt.Errorf("read turn %s: %w", f.Name(), err)
		}
		var t session.Turn
		err = json.Unmarshal(payload, &t)
		if err != nil {
			return nil, fmt.Errorf("unmarshal turn %s: %w", f.Name(), err)
		}
		turns = append(turns, t)
	}
	return turns, nil
}

// RecoverLatestIncomplete returns the last turn that has not yet completed.
func (s *Store) RecoverLatestIncomplete(sessionID string) (session.Turn, bool, error) {
	turns, err := s.LoadAll(sessionID)
	if err != nil {
		return session.Turn{}, false, err
	}
	for i := len(turns) - 1; i >= 0; i-- {
		if !turns[i].IsComplete() {
			return turns[i], true, nil
		}
	}
	return session.Turn{}, false, nil
}

// NextIndex returns the index to assign to the next new turn.
// Counts .json files in the turns directory without deserializing them.
func (s *Store) NextIndex(sessionID string) (int, error) {
	dir := s.turnsDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read turns directory: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	return count, nil
}
