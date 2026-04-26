package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Save(sess Session) error {
	err := os.MkdirAll(s.dir, 0o755)
	if err != nil {
		return fmt.Errorf("create sessions directory: %w", err)
	}

	payload, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	err = os.WriteFile(s.path(sess.ID), payload, 0o644)
	if err != nil {
		return fmt.Errorf("write session: %w", err)
	}

	return nil
}

func (s *Store) LoadLatest() (Session, bool, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Session{}, false, nil
		}
		return Session{}, false, fmt.Errorf("read sessions directory: %w", err)
	}

	files := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		files = append(files, entry)
	}

	if len(files) == 0 {
		return Session{}, false, nil
	}

	sort.Slice(files, func(i, j int) bool {
		infoI, errI := files[i].Info()
		infoJ, errJ := files[j].Info()
		if errI != nil || errJ != nil {
			return files[i].Name() > files[j].Name()
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})

	payload, err := os.ReadFile(filepath.Join(s.dir, files[0].Name()))
	if err != nil {
		return Session{}, false, fmt.Errorf("read session: %w", err)
	}

	var sess Session
	err = json.Unmarshal(payload, &sess)
	if err != nil {
		return Session{}, false, fmt.Errorf("unmarshal session: %w", err)
	}

	return sess, true, nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}
