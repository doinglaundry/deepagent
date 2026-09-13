package agentthread

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type RedisIncrByClient interface {
	IncrBy(ctx context.Context, key string, value int64) (int64, error)
}
type RedisSeqGenerator struct {
	client RedisIncrByClient
	prefix string
}

func NewRedisSeqGenerator(client RedisIncrByClient, prefix string) (generator *RedisSeqGenerator) {
	prefix = strings.Trim(strings.TrimSpace(prefix), ":/")
	if prefix == "" {
		prefix = "agentthread_history_seq"
	}
	return &RedisSeqGenerator{client: client, prefix: prefix}
}
func (g *RedisSeqGenerator) Next(ctx context.Context, threadID string) (sequence int64, err error) {
	if g == nil || g.client == nil {
		return 0, errors.New("agentthread: redis seq generator is not initialized")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return 0, errors.New("agentthread: thread id is required to generate history seq")
	}
	return g.client.IncrBy(ctx, fmt.Sprintf("%s:thread:%s", g.prefix, threadID), 1)
}
