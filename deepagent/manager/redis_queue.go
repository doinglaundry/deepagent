package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisQueue stores pending inputs and fencing leases. Queue operations are
// namespaced so multiple deployments can share one Redis instance.
type RedisQueue struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisQueue(client redis.UniversalClient, prefix string) *RedisQueue {
	if prefix == "" {
		prefix = "deepagent"
	}
	return &RedisQueue{client: client, prefix: prefix}
}
func (q *RedisQueue) key(id string) string      { return fmt.Sprintf("%s:pending:%s", q.prefix, id) }
func (q *RedisQueue) leaseKey(id string) string { return fmt.Sprintf("%s:lease:%s", q.prefix, id) }
func (q *RedisQueue) Enqueue(ctx context.Context, id string, input []byte) error {
	if q == nil || q.client == nil {
		return errors.New("redis queue is not initialized")
	}
	return q.client.RPush(ctx, q.key(id), input).Err()
}
func (q *RedisQueue) Has(ctx context.Context, id string) (bool, error) {
	if q == nil || q.client == nil {
		return false, errors.New("redis queue is not initialized")
	}
	n, e := q.client.LLen(ctx, q.key(id)).Result()
	return n > 0, e
}
func (q *RedisQueue) Pop(ctx context.Context, id string) ([]byte, error) {
	v, e := q.client.LPop(ctx, q.key(id)).Bytes()
	if e == redis.Nil {
		return nil, nil
	}
	return v, e
}
func (q *RedisQueue) Cancel(ctx context.Context, id string) error {
	return q.client.Del(ctx, q.key(id)).Err()
}

// InputLedger records accepted inputs until the owning run confirms them.
// A new Worker can call Recover after a crash and requeue the same payload.
type InputLedger struct {
	ID      string `json:"id"`
	Payload []byte `json:"payload"`
}

func (q *RedisQueue) acceptedKey(id string) string {
	return fmt.Sprintf("%s:accepted:%s", q.prefix, id)
}

func (q *RedisQueue) Accept(ctx context.Context, threadID string, item InputLedger) error {
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return q.client.HSet(ctx, q.acceptedKey(threadID), item.ID, b).Err()
}

func (q *RedisQueue) Confirm(ctx context.Context, threadID, inputID string) error {
	return q.client.HDel(ctx, q.acceptedKey(threadID), inputID).Err()
}

func (q *RedisQueue) Recover(ctx context.Context, threadID string) error {
	values, err := q.client.HVals(ctx, q.acceptedKey(threadID)).Result()
	if err != nil {
		return err
	}
	for _, raw := range values {
		var item InputLedger
		if json.Unmarshal([]byte(raw), &item) == nil {
			if err := q.Enqueue(ctx, threadID, item.Payload); err != nil {
				return err
			}
		}
	}
	return q.client.Del(ctx, q.acceptedKey(threadID)).Err()
}

func (q *RedisQueue) AcquireLease(ctx context.Context, threadID, workerID string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	token := make([]byte, 16)
	if _, e := rand.Read(token); e != nil {
		return "", e
	}
	lease := workerID + ":" + hex.EncodeToString(token)
	ok, e := q.client.SetNX(ctx, q.leaseKey(threadID), lease, ttl).Result()
	if e != nil {
		return "", e
	}
	if !ok {
		return "", errors.New("thread lease is held")
	}
	return lease, nil
}
func (q *RedisQueue) RenewLease(ctx context.Context, threadID, token string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = time.Minute
	}
	n, e := q.client.Eval(ctx, "if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) else return 0 end", []string{q.leaseKey(threadID)}, token, ttl.Milliseconds()).Int()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("lease token is stale")
	}
	return nil
}
func (q *RedisQueue) ReleaseLease(ctx context.Context, threadID, token string) error {
	n, e := q.client.Eval(ctx, "if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) else return 0 end", []string{q.leaseKey(threadID)}, token).Int()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("lease token is stale")
	}
	return nil
}
