// Package checkpointer stores the single Eino execution snapshot in an envelope.
package checkpointer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	redis "github.com/redis/go-redis/v9"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	e = os.MkdirAll(p, 0700)
	if e != nil {
		return nil, e
	}
	return &File{root: p}, nil
}

func (f *File) path(key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(f.root, hex.EncodeToString(h[:])+".json")
}

func (f *File) Get(ctx context.Context, key string) ([]byte, bool, error) {
	e := ctx.Err()
	if e != nil {
		return nil, false, e
	}
	validKeyE := validKey(key)
	if validKeyE != nil {
		return nil, false, validKeyE
	}
	data, err := os.ReadFile(f.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (f *File) Set(ctx context.Context, key string, data []byte) error {
	contextE := ctx.Err()
	if contextE != nil {
		return contextE
	}
	validKeyE := validKey(key)
	if validKeyE != nil {
		return validKeyE
	}
	temp, e := os.CreateTemp(f.root, ".checkpoint-*")
	if e != nil {
		return e
	}
	name := temp.Name()
	defer os.Remove(name)
	_, e = temp.Write(data)
	if e != nil {
		temp.Close()
		return e
	}
	e = temp.Sync()
	if e != nil {
		temp.Close()
		return e
	}
	e = temp.Close()
	if e != nil {
		return e
	}
	e = ctx.Err()
	if e != nil {
		return e
	}
	e = os.Rename(name, f.path(key))
	if e != nil {
		return e
	}
	dir, e := os.Open(f.root)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func validKey(key string) error {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key || strings.ContainsAny(key, "/\\") {
		return errors.New("checkpoint ID must be a nonempty path-free identifier")
	}
	return nil
}

type Redis struct {
	client rawClient
	prefix string
}

type rawClient interface {
	GetRaw(context.Context, string) ([]byte, error)
	SetRaw(context.Context, string, []byte, time.Duration) error
}

type universalRawClient struct{ redis.UniversalClient }

func (c universalRawClient) GetRaw(ctx context.Context, key string) ([]byte, error) {
	return c.Get(ctx, key).Bytes()
}

func (c universalRawClient) SetRaw(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.Set(ctx, key, value, ttl).Err()
}

func NewRedis(client redis.UniversalClient, prefix string) (*Redis, error) {
	if client == nil || strings.TrimSpace(prefix) == "" {
		return nil, errors.New("Redis checkpoint client and namespace prefix required")
	}
	return NewRaw(universalRawClient{client}, prefix)
}

func NewRaw(client rawClient, prefix string) (*Redis, error) {
	if client == nil || strings.TrimSpace(prefix) == "" {
		return nil, errors.New("Redis checkpoint client and namespace prefix required")
	}
	return &Redis{client: client, prefix: strings.TrimSuffix(prefix, ":") + ":"}, nil
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	e := validKey(key)
	if e != nil {
		return nil, false, e
	}
	data, err := r.client.GetRaw(ctx, r.prefix+key)
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (r *Redis) Set(ctx context.Context, key string, data []byte) error {
	e := validKey(key)
	if e != nil {
		return e
	}
	return r.client.SetRaw(ctx, r.prefix+key, data, 0)
}
