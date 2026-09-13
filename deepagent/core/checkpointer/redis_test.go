package checkpointer

import (
	"context"
	"eino-cli/protocol"
	"errors"
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
	prefix := "deepagent:test:checkpoint:" + protocol.NewID("ns")
	a, e := NewRedis(client, prefix)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := NewRedis(client, prefix+":other")
	defer client.Del(ctx, prefix+":key")
	if e = a.Set(ctx, "key", []byte("checkpoint state")); e != nil {
		t.Fatal(e)
	}
	v, e := a.Get(ctx, "key")
	if e != nil || string(v) != "checkpoint state" {
		t.Fatal(string(v), e)
	}
	if _, e = b.Get(ctx, "key"); !errors.Is(e, redis.Nil) {
		t.Fatal("namespace isolation", e)
	}
	if _, e = a.Get(ctx, "../key"); e == nil {
		t.Fatal("invalid identifier accepted")
	}
}
