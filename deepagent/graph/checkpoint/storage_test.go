package checkpointer

import (
	"context"
	"os"
	"testing"

	dalcache "eino-cli/deepagent/dal/cache"

	"github.com/google/uuid"
)

func TestRedisCheckpointsNamespaceAndRoundTrip(t *testing.T) {
	redisAddress := os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")
	if redisAddress == "" {
		t.Skip("set DEEPAGENT_TEST_REDIS_ADDR for Redis integration")
	}
	ctx := context.Background()
	client, err := dalcache.NewRedis(dalcache.RedisConfig{Addr: redisAddress})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "deepagent:test:checkpoint:" + uuid.NewString()
	redisStore, err := NewRedisStore(client, prefix)
	if err != nil {
		t.Fatal(err)
	}
	otherRedisStore, _ := NewRedisStore(client, prefix+":other")
	defer client.Del(ctx, prefix+":key")
	err = redisStore.Set(ctx, "key", []byte("checkpoint state"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, exists, err := redisStore.Get(ctx, "key")
	if err != nil || !exists || string(snapshot) != "checkpoint state" {
		t.Fatal(string(snapshot), err)
	}
	_, exists, err = otherRedisStore.Get(ctx, "key")
	if err != nil || exists {
		t.Fatal("namespace isolation", err)
	}
	_, _, err = redisStore.Get(ctx, "../key")
	if err == nil {
		t.Fatal("invalid identifier accepted")
	}
}

func TestFileCheckpointSharedAndKeysConfined(t *testing.T) {
	root := t.TempDir()
	firstFileStore, err := NewFile(root)
	if err != nil {
		t.Fatal(err)
	}
	secondFileStore, err := NewFile(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = firstFileStore.Set(ctx, "checkpoint", []byte(`{"run":"same"}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, exists, err := secondFileStore.Get(ctx, "checkpoint")
	if err != nil || !exists || string(snapshot) != `{"run":"same"}` {
		t.Fatal(string(snapshot), err)
	}
	err = firstFileStore.Set(ctx, "../../checkpoint", []byte("bad"))
	if err == nil {
		t.Fatal("traversal key accepted")
	}
	_, exists, err = secondFileStore.Get(ctx, "other")
	if err != nil || exists {
		t.Fatal("missing checkpoint exists", err)
	}
}
