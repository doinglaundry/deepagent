// Package runs persists completed CLI run records as JSON files under
// <RootDir>/.eino-cli/runs. The store is intentionally minimal — single
// directory, one file per run_id, atomic-rename writes. See
// specs/2026-05-19-cli-run-store/design.md.
package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Record is the on-disk wire format. Decoupled from runtime/run.Record
// so context.CancelFunc and error don't leak into JSON.
type Record struct {
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	Prompt        string    `json:"prompt,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
	Output        string    `json:"output,omitempty"`
	Error         string    `json:"error,omitempty"`
	Tokens        int64     `json:"tokens,omitempty"`
	Rollbackable  bool      `json:"rollbackable,omitempty"`
	RollbackPath  string    `json:"rollback_path,omitempty"`
	RollbackError string    `json:"rollback_error,omitempty"`
}

type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Save writes rec atomically via tmp file + rename, so Ctrl-C cannot leave
// a half-written JSON visible to List.
func (s *Store) Save(_ context.Context, rec Record) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create runs directory: %w", err)
	}
	payload, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run %s: %w", rec.ID, err)
	}
	final := s.path(rec.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("write run %s: %w", rec.ID, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename run %s: %w", rec.ID, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (Record, bool, error) {
	payload, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("read run %s: %w", id, err)
	}
	var rec Record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return Record{}, false, fmt.Errorf("decode run %s: %w", id, err)
	}
	return rec, true, nil
}

// List returns every persisted run, newest first by CreatedAt.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read runs directory: %w", err)
	}
	records := make([]Record, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		rec, ok, err := s.Get(ctx, id)
		if err != nil || !ok {
			continue
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	return records, nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}
