package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/google/uuid"
	redispkg "github.com/redis/go-redis/v9"
)

// Memory methods persist shared artifacts and fence writes with renewable Redis leases.
const memoryIndexKey = "deepagent:memory:index"

func (c *Manager) ClaimMemory(ctx context.Context, key, _ string, ttl time.Duration) (agentmodel.MemoryLease, error) {
	err := validMemoryRequest(key, ttl)
	if err != nil {
		return agentmodel.MemoryLease{}, err
	}
	lease := agentmodel.MemoryLease{Key: key, Token: uuid.NewString(), ExpiresAt: time.Now().Add(ttl)}
	const claim = `if redis.call('EXISTS', KEYS[1]) == 0 then redis.call('PSETEX', KEYS[1], ARGV[1], ARGV[2]); return 1 end; return 0`
	result, err := c.redis.Eval(ctx, claim, []string{memoryKey("lease", key)}, ttl.Milliseconds(), lease.Token)
	if err != nil {
		return agentmodel.MemoryLease{}, err
	}
	if result != int64(1) {
		return agentmodel.MemoryLease{}, agentmodel.ErrMemoryConflict
	}
	return lease, nil
}

func (c *Manager) RenewMemory(ctx context.Context, lease agentmodel.MemoryLease, ttl time.Duration) (agentmodel.MemoryLease, error) {
	err := validMemoryRequest(lease.Key, ttl)
	if err != nil || lease.Token == "" {
		if err != nil {
			return agentmodel.MemoryLease{}, err
		}
		return agentmodel.MemoryLease{}, agentmodel.ErrMemoryLeaseLost
	}
	const renew = `if redis.call('GET', KEYS[1]) == ARGV[1] then redis.call('PEXPIRE', KEYS[1], ARGV[2]); return 1 end; return 0`
	result, err := c.redis.Eval(ctx, renew, []string{memoryKey("lease", lease.Key)}, lease.Token, ttl.Milliseconds())
	if err != nil {
		return agentmodel.MemoryLease{}, err
	}
	if result != int64(1) {
		return agentmodel.MemoryLease{}, agentmodel.ErrMemoryLeaseLost
	}
	lease.ExpiresAt = time.Now().Add(ttl)
	return lease, nil
}

func (c *Manager) CompleteMemory(ctx context.Context, lease agentmodel.MemoryLease, version string, data []byte) error {
	encoded, err := json.Marshal(agentmodel.MemoryArtifact{Version: version, Data: data})
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
		return agentmodel.ErrMemoryLeaseLost
	}
	return nil
}

func (c *Manager) ReleaseMemory(ctx context.Context, lease agentmodel.MemoryLease) error {
	const release = `if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end; return 0`
	result, err := c.redis.Eval(ctx, release, []string{memoryKey("lease", lease.Key)}, lease.Token)
	if err != nil {
		return err
	}
	if result != int64(1) {
		return agentmodel.ErrMemoryLeaseLost
	}
	return nil
}

func (c *Manager) GetMemory(ctx context.Context, key string) (agentmodel.MemoryArtifact, error) {
	raw, err := c.redis.GetRaw(ctx, memoryKey("artifact", key))
	if errors.Is(err, redispkg.Nil) {
		return agentmodel.MemoryArtifact{}, agentmodel.ErrMemoryNotFound
	}
	if err != nil {
		return agentmodel.MemoryArtifact{}, err
	}
	var artifact agentmodel.MemoryArtifact
	err = json.Unmarshal(raw, &artifact)
	if err != nil {
		return agentmodel.MemoryArtifact{}, fmt.Errorf("decode memory artifact: %w", err)
	}
	return artifact, nil
}

func (c *Manager) ListMemory(ctx context.Context, prefix string, limit, offset int) (map[string]agentmodel.MemoryArtifact, error) {
	keys, err := c.redis.ZRange(ctx, memoryIndexKey, 0, -1)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	result := map[string]agentmodel.MemoryArtifact{}
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
		if errors.Is(getErr, agentmodel.ErrMemoryNotFound) {
			continue
		}
		if getErr != nil {
			return nil, getErr
		}
		result[key] = artifact
	}
	return result, nil
}

var _ agentmodel.MemoryStore = (*Manager)(nil)
