package checkpointer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	dalcache "eino-cli/deepagent/dal/cache"

	redis "github.com/redis/go-redis/v9"
)

// RedisStore 使用通用 Redis 客户端，适配 Eino 的字节存储接口。
type RedisStore struct {
	client dalcache.RedisClient
	prefix string
}

func NewRedisStore(client dalcache.RedisClient, prefix string) (*RedisStore, error) {
	if client == nil || strings.TrimSpace(prefix) == "" {
		return nil, errors.New("Redis checkpoint client and namespace prefix required")
	}
	return &RedisStore{client: client, prefix: strings.TrimSuffix(prefix, ":") + ":"}, nil
}

func (redisStore *RedisStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	keyErr := validateCheckpointKey(key)
	if keyErr != nil {
		return nil, false, keyErr
	}
	data, err := redisStore.client.GetRaw(ctx, redisStore.prefix+key)
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (redisStore *RedisStore) Set(ctx context.Context, key string, data []byte) error {
	err := validateCheckpointKey(key)
	if err != nil {
		return err
	}
	return redisStore.client.SetRaw(ctx, redisStore.prefix+key, data, 0)
}

type File struct{ root string }

func NewFile(root string) (*File, error) {
	if root == "" {
		return nil, errors.New("checkpoint directory required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	err = os.MkdirAll(absoluteRoot, 0700)
	if err != nil {
		return nil, err
	}
	return &File{root: absoluteRoot}, nil
}

func (fileStore *File) Get(ctx context.Context, key string) ([]byte, bool, error) {
	contextErr := ctx.Err()
	if contextErr != nil {
		return nil, false, contextErr
	}
	keyErr := validateCheckpointKey(key)
	if keyErr != nil {
		return nil, false, keyErr
	}
	data, err := os.ReadFile(fileStore.buildCheckpointPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (fileStore *File) Set(ctx context.Context, key string, data []byte) error {
	contextErr := ctx.Err()
	if contextErr != nil {
		return contextErr
	}
	keyErr := validateCheckpointKey(key)
	if keyErr != nil {
		return keyErr
	}
	temporaryFile, err := os.CreateTemp(fileStore.root, ".checkpoint-*")
	if err != nil {
		return err
	}
	temporaryPath := temporaryFile.Name()
	defer os.Remove(temporaryPath)
	_, err = temporaryFile.Write(data)
	if err != nil {
		temporaryFile.Close()
		return err
	}
	err = temporaryFile.Sync()
	if err != nil {
		temporaryFile.Close()
		return err
	}
	err = temporaryFile.Close()
	if err != nil {
		return err
	}
	err = ctx.Err()
	if err != nil {
		return err
	}
	err = os.Rename(temporaryPath, fileStore.buildCheckpointPath(key))
	if err != nil {
		return err
	}
	directory, err := os.Open(fileStore.root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (fileStore *File) buildCheckpointPath(key string) string {
	keyHash := sha256.Sum256([]byte(key))
	return filepath.Join(fileStore.root, hex.EncodeToString(keyHash[:])+".json")
}

func validateCheckpointKey(key string) error {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key || strings.ContainsAny(key, "/\\") {
		return errors.New("checkpoint ID must be a nonempty path-free identifier")
	}
	return nil
}
