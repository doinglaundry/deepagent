package checkpointer

import (
	"context"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
)

func TestFileCheckpointSharedAndKeysConfined(t *testing.T) {
	root := t.TempDir()
	a, e := NewFile(root)
	if e != nil {
		t.Fatal(e)
	}
	b, e := NewFile(root)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	e = a.Set(ctx, "checkpoint", []byte(`{"run":"same"}`))
	if e != nil {
		t.Fatal(e)
	}
	v, exists, e := b.Get(ctx, "checkpoint")
	if e != nil || !exists || string(v) != `{"run":"same"}` {
		t.Fatal(string(v), e)
	}
	e = a.Set(ctx, "../../checkpoint", []byte("bad"))
	if e == nil {
		t.Fatal("traversal key accepted")
	}
	_, exists, e = b.Get(ctx, "other")
	if e != nil || exists {
		t.Fatal("missing checkpoint exists", e)
	}
}
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
	e = a.Set(ctx, "key", []byte("checkpoint state"))
	if e != nil {
		t.Fatal(e)
	}
	v, exists, e := a.Get(ctx, "key")
	if e != nil || !exists || string(v) != "checkpoint state" {
		t.Fatal(string(v), e)
	}
	_, exists, e = b.Get(ctx, "key")
	if e != nil || exists {
		t.Fatal("namespace isolation", e)
	}
	_, _, e = a.Get(ctx, "../key")
	if e == nil {
		t.Fatal("invalid identifier accepted")
	}
}
