package checkpoint

import (
	"context"
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
	err := os.MkdirAll(s.dir, 0o755)
	if err != nil {
		return fmt.Errorf("create checkpoints directory: %w", err)
	}

	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	err = os.WriteFile(s.path(snapshot.SessionID), payload, 0o644)
	if err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}

	return nil
}

func (s *Store) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
	path := s.path(checkPointID)
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read checkpoint: %w", err)
	}
	return payload, true, nil
}

func (s *Store) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
	err := os.MkdirAll(s.dir, 0o755)
	if err != nil {
		return fmt.Errorf("create checkpoints directory: %w", err)
	}
	err = os.WriteFile(s.path(checkPointID), checkPoint, 0o644)
	if err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return nil
}

func (s *Store) path(checkPointID string) string {
	return filepath.Join(s.dir, checkPointID+".json")
}
