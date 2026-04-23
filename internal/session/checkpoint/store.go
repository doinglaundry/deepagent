package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Snapshot struct {
	SessionID      string    `json:"session_id"`
	WorkspaceRoot  string    `json:"workspace_root"`
	LastInput      string    `json:"last_input,omitempty"`
	AwaitingApproval bool    `json:"awaiting_approval"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Save(snapshot Snapshot) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create checkpoints directory: %w", err)
	}

	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	if err := os.WriteFile(s.path(snapshot.SessionID), payload, 0o644); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}

	return nil
}

func (s *Store) path(sessionID string) string {
	return filepath.Join(s.dir, sessionID+".json")
}
