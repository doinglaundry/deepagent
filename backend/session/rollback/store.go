package rollback

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store struct {
	root string
}

type Snapshot struct {
	RunID     string          `json:"run_id"`
	CreatedAt time.Time       `json:"created_at"`
	History   json.RawMessage `json:"history,omitempty"`
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) SavePost(ctx context.Context, runID string, history []byte) (string, error) {
	base := s.snapshotDir(runID)
	tmp := base + ".tmp"
	final := filepath.Join(base, "post")
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", fmt.Errorf("create rollback snapshot: %w", err)
	}
	if err := writeSnapshotMeta(tmp, runID, history); err != nil {
		return "", err
	}
	if err := s.copyControlledRoots(ctx, tmp); err != nil {
		return "", err
	}
	if err := os.RemoveAll(final); err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", fmt.Errorf("publish rollback snapshot: %w", err)
	}
	return final, nil
}

func (s *Store) RestorePost(ctx context.Context, runID string) ([]byte, error) {
	dir := filepath.Join(s.snapshotDir(runID), "post")
	payload, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return nil, fmt.Errorf("read rollback snapshot: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return nil, fmt.Errorf("decode rollback snapshot: %w", err)
	}
	if err := s.restoreControlledRoots(ctx, dir); err != nil {
		return nil, err
	}
	return append([]byte(nil), snap.History...), nil
}

func (s *Store) snapshotDir(runID string) string {
	return filepath.Join(s.root, ".eino-cli", "rollback", runID)
}

func writeSnapshotMeta(dir, runID string, history []byte) error {
	snap := Snapshot{RunID: runID, CreatedAt: time.Now(), History: append([]byte(nil), history...)}
	payload, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rollback snapshot: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), payload, 0o644); err != nil {
		return fmt.Errorf("write rollback snapshot: %w", err)
	}
	return nil
}

func (s *Store) copyControlledRoots(ctx context.Context, dst string) error {
	for _, pair := range s.fixedRoots() {
		if err := copyDirIfExists(ctx, pair.host, filepath.Join(dst, pair.name)); err != nil {
			return err
		}
	}
	for _, host := range s.skillDirs() {
		if err := copyDirIfExists(ctx, host, filepath.Join(dst, filepath.Base(host))); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) restoreControlledRoots(ctx context.Context, src string) error {
	for _, pair := range s.fixedRoots() {
		if err := restoreDir(ctx, filepath.Join(src, pair.name), pair.host); err != nil {
			return err
		}
	}
	for _, host := range s.skillDirs() {
		if err := os.RemoveAll(host); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "skill-") {
			continue
		}
		if err := copyDir(ctx, filepath.Join(src, entry.Name()), filepath.Join(s.einoDir(), entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

type rootPair struct {
	name string
	host string
}

func (s *Store) fixedRoots() []rootPair {
	return []rootPair{
		{name: "checkpoints", host: filepath.Join(s.einoDir(), "checkpoints")},
		{name: "user-data", host: filepath.Join(s.einoDir(), "users", "local", "threads", "cli", "user-data")},
		{name: "memory", host: filepath.Join(s.einoDir(), "memory")},
		{name: "runs", host: filepath.Join(s.einoDir(), "runs")},
	}
}

func (s *Store) einoDir() string {
	return filepath.Join(s.root, ".eino-cli")
}

func (s *Store) skillDirs() []string {
	entries, err := os.ReadDir(s.einoDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "skill-") {
			out = append(out, filepath.Join(s.einoDir(), entry.Name()))
		}
	}
	return out
}

func restoreDir(ctx context.Context, src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return copyDir(ctx, src, dst)
}

func copyDirIfExists(ctx context.Context, src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return copyDir(ctx, src, dst)
}

func copyDir(ctx context.Context, src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
