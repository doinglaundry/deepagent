package deepagents

import (
	"context"

	"eino-cli/deepagent/core/internal/conversation"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type HistoryRolloutStore = conversation.HistoryRolloutStore
type HistoryRecord = conversation.HistoryRecord
type CompactSnapshot = conversation.CompactSnapshot
type HistoryRecordExtend = conversation.HistoryRecordExtend
type ListQuery = conversation.ListQuery
type CompactionStrategy = conversation.CompactionStrategy
type CompactRecord = conversation.CompactRecord
type CompactionResult = conversation.CompactionResult
type ResumeResult = conversation.ResumeResult
type TokenCounter = conversation.TokenCounter
type HistoryRecordIDProvider = conversation.HistoryRecordIDProvider
type ContextUsageSnapshot = conversation.ContextUsageSnapshot

// ContextManager preserves the Thread context contract.
type ContextManager interface {
	ReloadHistory(context.Context) error
	AddHistory(context.Context, string, ...*schema.Message) error
	History(context.Context) []*schema.Message
	ContextUsage() ContextUsageSnapshot
	RecordModelUsage(context.Context, *model.TokenUsage)
	Compact(context.Context, string) (*ContextCompactedPayload, error)
	CompactNeeded(context.Context) bool
}

// The default context also provides BuildRequest for the
var _ Conversation = (*conversation.Conversation)(nil)

// Storage and compaction implementations belong to Conversation. These aliases
// expose the existing construction API at the Thread boundary.
type SummaryCompaction = conversation.SummaryCompaction
type GormHistoryRolloutStore = conversation.GormHistoryRolloutStore
type SeqGenerator = conversation.SeqGenerator
type RedisIncrByClient = conversation.RedisIncrByClient
type RedisSeqGenerator = conversation.RedisSeqGenerator
type ListOrder = conversation.ListOrder

const (
	ListOrderASC  = conversation.ListOrderASC
	ListOrderDESC = conversation.ListOrderDESC
)

var NewGormHistoryRolloutStore = conversation.NewGormHistoryRolloutStore
var NewRedisSeqGenerator = conversation.NewRedisSeqGenerator
