package model

import (
	"context"
	"time"

	redis "github.com/redis/go-redis/v9"
)

type RedisClient interface {
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Get(ctx context.Context, key string, dest any) error
	MGet(ctx context.Context, keys []string, dest any) error
	GetRaw(ctx context.Context, key string) ([]byte, error)
	SetRaw(ctx context.Context, key string, value []byte, ttl time.Duration) error
	ZAdd(ctx context.Context, key string, members []redis.Z) (int64, error)
	ZRange(ctx context.Context, key string, start, stop int64) ([]string, error)
	ZRem(ctx context.Context, key string, members ...any) (int64, error)
	MoveZSet(ctx context.Context, from, to, member string, score float64) error
	IncrBy(ctx context.Context, key string, delta int64) (int64, error)
	GetCounter(ctx context.Context, key string) (int64, error)
	Del(ctx context.Context, keys ...string) (int64, error)
	Publish(ctx context.Context, channel string, payload []byte) error
	Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error)
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
}
