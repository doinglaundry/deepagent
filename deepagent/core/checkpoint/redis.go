package checkpointer

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
	"path/filepath"
	"strings"
	"time"
)

type Store interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte) error
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
	if e := validKey(key); e != nil {
		return nil, false, e
	}
	data, err := r.client.GetRaw(ctx, r.prefix+key)
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	return data, err == nil, err
}
func (r *Redis) Set(ctx context.Context, key string, data []byte) error {
	if e := validKey(key); e != nil {
		return e
	}
	return r.client.SetRaw(ctx, r.prefix+key, data, 0)
}
