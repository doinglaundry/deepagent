package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/bytedance/sonic"
	redispkg "github.com/redis/go-redis/v9"
)

const (
	redisDefaultReadTimeout  = 500 * time.Millisecond
	redisDefaultWriteTimeout = 500 * time.Millisecond
	redisInitRetryCount      = 3
	redisInitRetryWait       = 100 * time.Millisecond
)

type RedisConfig struct {
	Addr         string
	Password     string
	DB           int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

type redisClient struct{ client *redispkg.Client }

var redisClientCache sync.Map

func NewRedis(cfg RedisConfig) (agentmodel.RedisClient, error) {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, errors.New("redis address is required")
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = redisDefaultReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = redisDefaultWriteTimeout
	}
	cached, ok := redisClientCache.Load(cfg)
	if ok {
		return cached.(*redisClient), nil
	}
	cli := redispkg.NewClient(&redispkg.Options{
		Addr: cfg.Addr, Password: cfg.Password, DB: cfg.DB,
		ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout,
		Protocol: 2, DisableIdentity: true, MaxRetries: -1,
	})
	var err error
	for i := 0; i < redisInitRetryCount; i++ {
		err = cli.Ping(context.Background()).Err()
		if err == nil {
			actual, loaded := redisClientCache.LoadOrStore(cfg, &redisClient{client: cli})
			if loaded {
				_ = cli.Close()
			}
			return actual.(*redisClient), nil
		}
		if i+1 < redisInitRetryCount {
			time.Sleep(redisInitRetryWait)
		}
	}
	_ = cli.Close()
	return nil, err
}

func (c *redisClient) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	encoded, err := sonic.Marshal(value)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key, encoded, ttl).Err()
}

func (c *redisClient) Get(ctx context.Context, key string, dest any) error {
	raw, err := c.client.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	return sonic.UnmarshalString(raw, dest)
}

func (c *redisClient) MGet(ctx context.Context, keys []string, dest any) error {
	if len(keys) == 0 {
		return nil
	}
	items, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		return err
	}
	payloads := make([]string, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case nil:
			payloads = append(payloads, "null")
		case string:
			payloads = append(payloads, value)
		case []byte:
			payloads = append(payloads, string(value))
		default:
			return fmt.Errorf("unexpected redis mget value type %T", item)
		}
	}
	return sonic.UnmarshalString("["+strings.Join(payloads, ",")+"]", dest)
}

func (c *redisClient) GetRaw(ctx context.Context, key string) ([]byte, error) {
	raw, err := c.client.Get(ctx, key).Bytes()
	return raw, err
}

func (c *redisClient) SetRaw(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

func (c *redisClient) ZAdd(ctx context.Context, key string, members []redispkg.Z) (int64, error) {
	if len(members) == 0 {
		return 0, nil
	}
	return c.client.ZAdd(ctx, key, members...).Result()
}

func (c *redisClient) ZRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	return c.client.ZRange(ctx, key, start, stop).Result()
}

func (c *redisClient) ZRem(ctx context.Context, key string, members ...any) (int64, error) {
	if len(members) == 0 {
		return 0, nil
	}
	return c.client.ZRem(ctx, key, members...).Result()
}

// MoveZSet records the destination before removing the source in one Redis operation.
func (c *redisClient) MoveZSet(ctx context.Context, from, to, member string, score float64) error {
	const move = `redis.call('ZADD', KEYS[2], ARGV[1], ARGV[2]); redis.call('ZREM', KEYS[1], ARGV[2]); return 1`
	return c.client.Eval(ctx, move, []string{from, to}, score, member).Err()
}

func (c *redisClient) IncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	return c.client.IncrBy(ctx, key, delta).Result()
}

func (c *redisClient) GetCounter(ctx context.Context, key string) (int64, error) {
	val, err := c.client.Get(ctx, key).Int64()
	if errors.Is(err, redispkg.Nil) {
		return 0, nil
	}
	return val, err
}

func (c *redisClient) Del(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	return c.client.Del(ctx, keys...).Result()
}

func (c *redisClient) Publish(ctx context.Context, channel string, payload []byte) error {
	return c.client.Publish(ctx, channel, payload).Err()
}

func (c *redisClient) Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error) {
	pubsub := c.client.Subscribe(ctx, channel)
	_, receiveErr := pubsub.Receive(ctx)
	if receiveErr != nil {
		_ = pubsub.Close()
		return nil, nil, receiveErr
	}
	out := make(chan []byte, 32)
	go func() {
		defer close(out)
		for {
			message, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				return
			}
			select {
			case out <- []byte(message.Payload):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, pubsub.Close, nil
}

func (c *redisClient) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return c.client.Eval(ctx, script, keys, args...).Result()
}
