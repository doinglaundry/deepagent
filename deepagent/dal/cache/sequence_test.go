package cache

import (
	"context"
	"errors"
	"math"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type sequenceRedis struct {
	RedisClient
	mu       sync.Mutex
	counters map[string]int64
	failure  error
}

func (redis *sequenceRedis) IncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	redis.mu.Lock()
	defer redis.mu.Unlock()
	err := ctx.Err()
	if err != nil {
		return 0, err
	}
	if redis.failure != nil {
		return 0, redis.failure
	}
	if redis.counters == nil {
		redis.counters = make(map[string]int64)
	}
	redis.counters[key] += delta
	return redis.counters[key], nil
}

func TestGenerateIDPreservesExistingCounterAndBounds(t *testing.T) {
	const base int64 = 2_000_000_000_000_000_000
	const key = "deepagent:coordinator:global_id"
	redis := &sequenceRedis{counters: map[string]int64{key: 41}}
	id, err := GenerateID(context.Background(), redis)
	if err != nil || id != base+42 {
		t.Fatalf("existing ID counter changed: id=%d err=%v", id, err)
	}
	redis.counters[key] = math.MaxInt64 - base - 1
	id, err = GenerateID(context.Background(), redis)
	if err != nil || id != math.MaxInt64 {
		t.Fatalf("last valid ID: id=%d err=%v", id, err)
	}
	id, err = GenerateID(context.Background(), redis)
	if err == nil || id != 0 {
		t.Fatalf("ID overflow was accepted: id=%d err=%v", id, err)
	}
}

func TestGenerateSequenceSeparatesScopesAndConcurrentCallers(t *testing.T) {
	redis := &sequenceRedis{counters: map[string]int64{"thread-a": 8}}
	sequence, err := GenerateSequence(context.Background(), redis, "thread-b")
	if err != nil || sequence != 1 {
		t.Fatalf("new scope: sequence=%d err=%v", sequence, err)
	}
	results := make(chan int64, 64)
	failures := make(chan error, 64)
	var workers sync.WaitGroup
	for index := 0; index < 64; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			sequence, err := GenerateSequence(context.Background(), redis, "thread-a")
			if err != nil {
				failures <- err
				return
			}
			results <- sequence
		}()
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	seen := make(map[int64]bool)
	for sequence := range results {
		if seen[sequence] || sequence <= 8 || sequence > 72 {
			t.Fatalf("invalid or repeated sequence %d", sequence)
		}
		seen[sequence] = true
	}
	if len(seen) != 64 || redis.counters["thread-b"] != 1 {
		t.Fatal("concurrent callers lost increments or changed another scope")
	}
}

func TestGenerateSequencePropagatesFailures(t *testing.T) {
	failure := errors.New("redis unavailable")
	_, err := GenerateSequence(context.Background(), &sequenceRedis{failure: failure}, "thread")
	if !errors.Is(err, failure) {
		t.Fatalf("storage error was hidden: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = GenerateSequence(ctx, &sequenceRedis{}, "thread")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was hidden: %v", err)
	}
	_, err = GenerateSequence(context.Background(), nil, "thread")
	if err == nil {
		t.Fatal("missing Redis was accepted")
	}
	_, err = GenerateSequence(context.Background(), &sequenceRedis{counters: map[string]int64{"thread": -1}}, "thread")
	if err == nil {
		t.Fatal("non-positive sequence was accepted")
	}
}

func TestGenerateSequenceRealRedis(t *testing.T) {
	addr := os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set DEEPAGENT_TEST_REDIS_ADDR for Redis sequence validation")
	}
	redis, err := NewRedis(RedisConfig{Addr: addr})
	if err != nil {
		t.Fatal(err)
	}
	key := "deepagent:test:sequence:" + uuid.NewString()
	t.Cleanup(func() { _, _ = redis.Del(context.Background(), key) })
	const count = 64
	results := make(chan int64, count)
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			sequence, err := GenerateSequence(context.Background(), redis, key)
			if err != nil {
				t.Error(err)
				return
			}
			results <- sequence
		}()
	}
	workers.Wait()
	close(results)
	seen := make(map[int64]bool)
	for sequence := range results {
		if seen[sequence] || sequence < 1 || sequence > count {
			t.Fatalf("repeated or invalid Redis sequence: %d", sequence)
		}
		seen[sequence] = true
	}
	value, err := redis.GetCounter(context.Background(), key)
	if err != nil || value != count || len(seen) != count {
		t.Fatalf("concurrent Redis increments: value=%d distinct=%d err=%v", value, len(seen), err)
	}
}
