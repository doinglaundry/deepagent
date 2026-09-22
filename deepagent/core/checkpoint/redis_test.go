package checkpointer

import (
	"context"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
)

func TestRedisCheckpointsNamespaceAndRoundTrip(t *testing.T) {
	addr := os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set DEEPAGENT_TEST_REDIS_ADDR for Redis integration")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	prefix := "deepagent:test:checkpoint:" + uuid.NewString()
	a, e := NewRedis(client, prefix)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := NewRedis(client, prefix+":other")
	defer client.Del(ctx, prefix+":key")
	if e = a.Set(ctx, "key", []byte("checkpoint state")); e != nil {
		t.Fatal(e)
	}
	v, exists, e := a.Get(ctx, "key")
	if e != nil || !exists || string(v) != "checkpoint state" {
		t.Fatal(string(v), e)
	}
	if _, exists, e = b.Get(ctx, "key"); e != nil || exists {
		t.Fatal("namespace isolation", e)
	}
	if _, _, e = a.Get(ctx, "../key"); e == nil {
		t.Fatal("invalid identifier accepted")
	}
}
