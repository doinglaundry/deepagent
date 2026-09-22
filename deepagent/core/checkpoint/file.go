// Package checkpointer provides atomic file checkpoints for workers on a shared volume.
// Distributed deployments can instead provide the same Get/Set interface using Manager.
package checkpointer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

type File struct{ root string }

func NewFile(root string) (*File, error) {
	if root == "" {
		return nil, errors.New("checkpoint directory required")
	}
	p, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(p, 0700); e != nil {
		return nil, e
	}
	return &File{root: p}, nil
}
func (f *File) path(key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(f.root, hex.EncodeToString(h[:])+".json")
}
func (f *File) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if e := ctx.Err(); e != nil {
		return nil, false, e
	}
	if e := validKey(key); e != nil {
		return nil, false, e
	}
	data, err := os.ReadFile(f.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}
func (f *File) Set(ctx context.Context, key string, data []byte) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := validKey(key); e != nil {
		return e
	}
	temp, e := os.CreateTemp(f.root, ".checkpoint-*")
	if e != nil {
		return e
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, e = temp.Write(data); e != nil {
		temp.Close()
		return e
	}
	if e = temp.Sync(); e != nil {
		temp.Close()
		return e
	}
	if e = temp.Close(); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = os.Rename(name, f.path(key)); e != nil {
		return e
	}
	dir, e := os.Open(f.root)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
