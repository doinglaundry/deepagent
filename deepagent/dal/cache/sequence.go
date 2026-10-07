package cache

import (
	"context"
	"errors"
)

var ErrRedisUnavailable = errors.New("redis unavailable")

// GenerateSequence 在指定 Redis key 下生成递增序号，各作用域使用独立的 key。
func GenerateSequence(ctx context.Context, redis RedisClient, key string) (int64, error) {
	if redis == nil {
		return 0, ErrRedisUnavailable
	}
	sequence, err := redis.IncrBy(ctx, key, 1)
	if err != nil {
		return 0, err
	}
	if sequence <= 0 {
		return 0, errors.New("sequence must be positive")
	}
	return sequence, nil
}

// GenerateID 使用已有全局计数和偏移量，生成持久化实体的唯一 ID。
func GenerateID(ctx context.Context, redis RedisClient) (int64, error) {
	sequence, err := GenerateSequence(ctx, redis, "deepagent:coordinator:global_id")
	if err != nil {
		return 0, err
	}
	const base int64 = 2_000_000_000_000_000_000
	if sequence > int64(^uint64(0)>>1)-base {
		return 0, errors.New("distributed ID counter overflow")
	}
	return base + sequence, nil
}
