package checkpoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
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
