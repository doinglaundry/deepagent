package distributed

import (
	"context"
	"eino-cli/deepagent/core/checkpointer"
	"eino-cli/deepagent/core/engine/agentthread"
	"eino-cli/manager/api"
	"fmt"
	"github.com/redis/go-redis/v9"
	"path/filepath"
	"time"
)

type CheckpointConfig struct {
	Backend string `yaml:"backend"`
	Path    string `yaml:"path"`
}

func newCheckpointStore(ctx context.Context, m api.Manager, c Config) (agentthread.Checkpoints, func(), error) {
	noop := func() {}
	switch c.Checkpoint.Backend {
	case "", "mysql":
		return Checkpoints{Manager: m}, noop, nil
	case "file":
		if c.Checkpoint.Path == "" {
			return nil, nil, fmt.Errorf("checkpoint.path required for file storage")
		}
		store, err := checkpointer.NewFile(filepath.Join(c.Checkpoint.Path, safeComponent(c.Manager.Namespace)))
		return store, noop, err
	case "redis":
		client := redis.NewClient(&redis.Options{Addr: c.Manager.RedisAddr, Password: c.Manager.RedisPassword, DB: c.Manager.RedisDB})
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := client.Ping(checkCtx).Err(); err != nil {
			_ = client.Close()
			return nil, nil, fmt.Errorf("connect checkpoint Redis: %w", err)
		}
		store, err := checkpointer.NewRedis(client, "deepagent:"+safeComponent(c.Manager.Namespace)+":checkpoints:")
		if err != nil {
			_ = client.Close()
			return nil, nil, err
		}
		return store, func() { _ = client.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("checkpoint.backend must be mysql, redis, or file")
	}
}
