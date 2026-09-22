package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	memorypkg "eino-cli/deepagent/protocol/memory"
	"github.com/google/uuid"
	redispkg "github.com/redis/go-redis/v9"
)

const memoryIndexKey = "deepagent:memory:index"

func memoryKey(kind, key string) string {
	sum := sha256.Sum256([]byte(key))
	return "deepagent:memory:" + kind + ":" + hex.EncodeToString(sum[:])
}

func validMemoryRequest(key string, ttl time.Duration) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("memory key is required")
	}
	if ttl <= 0 {
		return errors.New("memory lease duration must be positive")
	}
	return nil
}

func (c *Manager) ClaimMemory(ctx context.Context, key, _ string, ttl time.Duration) (memorypkg.Lease, error) {
	if err := validMemoryRequest(key, ttl); err != nil {
		return memorypkg.Lease{}, err
	}
	lease := memorypkg.Lease{Key: key, Token: uuid.NewString(), ExpiresAt: time.Now().Add(ttl)}
	const claim = `if redis.call('EXISTS', KEYS[1]) == 0 then redis.call('PSETEX', KEYS[1], ARGV[1], ARGV[2]); return 1 end; return 0`
	result, err := c.redis.Eval(ctx, claim, []string{memoryKey("lease", key)}, ttl.Milliseconds(), lease.Token)
	if err != nil {
		return memorypkg.Lease{}, err
	}
	if result != int64(1) {
		return memorypkg.Lease{}, memorypkg.ErrConflict
	}
	return lease, nil
}

func (c *Manager) RenewMemory(ctx context.Context, lease memorypkg.Lease, ttl time.Duration) (memorypkg.Lease, error) {
	if err := validMemoryRequest(lease.Key, ttl); err != nil || lease.Token == "" {
		if err != nil {
			return memorypkg.Lease{}, err
		}
		return memorypkg.Lease{}, memorypkg.ErrLeaseLost
	}
	const renew = `if redis.call('GET', KEYS[1]) == ARGV[1] then redis.call('PEXPIRE', KEYS[1], ARGV[2]); return 1 end; return 0`
	result, err := c.redis.Eval(ctx, renew, []string{memoryKey("lease", lease.Key)}, lease.Token, ttl.Milliseconds())
	if err != nil {
		return memorypkg.Lease{}, err
	}
	if result != int64(1) {
		return memorypkg.Lease{}, memorypkg.ErrLeaseLost
	}
	lease.ExpiresAt = time.Now().Add(ttl)
	return lease, nil
}

func (c *Manager) CompleteMemory(ctx context.Context, lease memorypkg.Lease, version string, data []byte) error {
	encoded, err := json.Marshal(memorypkg.Artifact{Version: version, Data: data})
	if err != nil {
		return err
	}
	const complete = `if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end; redis.call('SET', KEYS[2], ARGV[2]); redis.call('ZADD', KEYS[3], ARGV[3], ARGV[4]); return 1`
	result, err := c.redis.Eval(ctx, complete, []string{
		memoryKey("lease", lease.Key), memoryKey("artifact", lease.Key), memoryIndexKey,
	}, lease.Token, encoded, time.Now().UnixMilli(), lease.Key)
	if err != nil {
		return err
	}
	if result != int64(1) {
		return memorypkg.ErrLeaseLost
	}
	return nil
}

func (c *Manager) ReleaseMemory(ctx context.Context, lease memorypkg.Lease) error {
	const release = `if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end; return 0`
	result, err := c.redis.Eval(ctx, release, []string{memoryKey("lease", lease.Key)}, lease.Token)
	if err != nil {
		return err
	}
	if result != int64(1) {
		return memorypkg.ErrLeaseLost
	}
	return nil
}

func (c *Manager) GetMemory(ctx context.Context, key string) (memorypkg.Artifact, error) {
	raw, err := c.redis.GetRaw(ctx, memoryKey("artifact", key))
	if errors.Is(err, redispkg.Nil) {
		return memorypkg.Artifact{}, memorypkg.ErrNotFound
	}
	if err != nil {
		return memorypkg.Artifact{}, err
	}
	var artifact memorypkg.Artifact
	if err = json.Unmarshal(raw, &artifact); err != nil {
		return memorypkg.Artifact{}, fmt.Errorf("decode memory artifact: %w", err)
	}
	return artifact, nil
}

func (c *Manager) ListMemory(ctx context.Context, prefix string, limit, offset int) (map[string]memorypkg.Artifact, error) {
	keys, err := c.redis.ZRange(ctx, memoryIndexKey, 0, -1)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	result := map[string]memorypkg.Artifact{}
	skipped := 0
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		if limit > 0 && len(result) >= limit {
			break
		}
		artifact, getErr := c.GetMemory(ctx, key)
		if errors.Is(getErr, memorypkg.ErrNotFound) {
			continue
		}
		if getErr != nil {
			return nil, getErr
		}
		result[key] = artifact
	}
	return result, nil
}

var _ memorypkg.Store = (*Manager)(nil)
