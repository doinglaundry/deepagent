package checkpointer

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
	"path/filepath"
	"strings"
)

type Store interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte) error
}

func validKey(key string) error {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key || strings.ContainsAny(key, "/\\") {
		return errors.New("checkpoint ID must be a nonempty path-free identifier")
	}
	return nil
}

type Redis struct {
	client redis.UniversalClient
	prefix string
}

func NewRedis(client redis.UniversalClient, prefix string) (*Redis, error) {
	if client == nil || strings.TrimSpace(prefix) == "" {
		return nil, errors.New("Redis checkpoint client and namespace prefix required")
	}
	return &Redis{client: client, prefix: strings.TrimSuffix(prefix, ":") + ":"}, nil
}
func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	if e := validKey(key); e != nil {
		return nil, e
	}
	return r.client.Get(ctx, r.prefix+key).Bytes()
}
func (r *Redis) Set(ctx context.Context, key string, data []byte) error {
	if e := validKey(key); e != nil {
		return e
	}
	return r.client.Set(ctx, r.prefix+key, data, 0).Err()
}
