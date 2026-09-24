package conversation

import (
	"context"
	"github.com/cloudwego/eino/schema"
)

type Message = schema.Message
type TokenCounter func(messages []*schema.Message) int

type HistoryRecordType string

const (
	HistoryRecordMessage HistoryRecordType = "message"
	HistoryRecordCompact HistoryRecordType = "compact"
)

type HistoryRecord struct {
	Type       HistoryRecordType
	ThreadID   string
	RunID      string `json:"TurnID" yaml:"turnid"`
	UniqueKey  string
	MessageID  int64
	Seq        int64
	Message    *Message
	CreateAt   int64 // unix timestamp, second
	CreateAtMS int64 // unix timestamp, millisecond
	Ext        *HistoryRecordExtend
}

func (r *HistoryRecord) OrderSeq() int64 {
	if r == nil {
		return 0
	}
	if r.Seq > 0 {
		return r.Seq
	}
	return r.MessageID
}

type HistoryRecordExtend struct {
	CompactStrategyID      string
	CompactStrategyPayload string
}

type ContextState struct {
	ThreadID    string
	Usage       ContextUsageSnapshot
	UpdatedAtMS int64
}

type ContextStateStore interface {
	Save(ctx context.Context, state ContextState) error
}

// CompactRecord 是压缩锚点记录，策略特有信息通过 payload 透传。
type CompactRecord struct {
	Summary *Message

	CompactStrategyID      string
	CompactStrategyPayload string
}

type CompactionResult struct {
	Compact *CompactRecord
	Rebuilt []*Message
}

type ResumeResult struct {
	Rebuilt []*Message
}

type ContextUsageSource string

const (
	// ContextUsageSourceEstimated means the current value is derived entirely from
	// the local TokenCounter. This is used before the first model response and
	// after history reload or compaction.
	ContextUsageSourceEstimated ContextUsageSource = "estimated"
	// ContextUsageSourceModelUsage means the baseline is the provider-reported
	// usage from the last successful model response, plus locally estimated
	// messages added after that response.
	ContextUsageSourceModelUsage ContextUsageSource = "model_usage"
)

// Backward-compatible aliases for callers that have already referenced the
// original token-usage names.
type TokenUsageSource = ContextUsageSource

const (
	TokenUsageSourceEstimated  = ContextUsageSourceEstimated
	TokenUsageSourceModelUsage = ContextUsageSourceModelUsage
)

type ContextUsageSnapshot struct {
	// ContextWindow is the configured model context window. A zero value means
	// the caller did not provide the model window.
	ContextWindow int64
	// LastModelTotal is the provider-reported total token usage from the last
	// successful model response. It is the authoritative baseline when Source is
	// model_usage.
	LastModelTotal int64
	// EstimatedAfterLastModel is the locally estimated token count for user/tool
	// messages appended after the last model response. Those messages are not
	// covered by LastModelTotal yet.
	EstimatedAfterLastModel int64
	// CurrentTotal is the value used by compact triggering.
	CurrentTotal int64
	// Source describes whether CurrentTotal came from local estimation or
	// provider-reported usage.
	Source ContextUsageSource
	// LastModelPromptTokens is retained for observability/debugging. It is not
	// used as the compact trigger.
	LastModelPromptTokens int64
	// LastModelCompletionTokens is retained for observability/debugging. It is
	// not used as the compact trigger.
	LastModelCompletionTokens int64
}

// AutoCompactLimiter is an optional capability for compaction strategies that
// want ContextManager to perform usage-based trigger checks before Compact.
type AutoCompactLimiter interface{ AutoCompactTokenLimit() int64 }

// CompactionStrategy 同时负责压缩与恢复。
type CompactionStrategy interface {
	ID() string
	Compact(ctx context.Context, current []*Message) (*CompactionResult, error)
	// Resume 从压缩记录中恢复上下文。
	/*
	  1. [m1,m2,m3...mn]
	  2. 压缩后 rollout 变成 [m1,m2,m3...mn,summary_record]
	  3. 继续往前走 [m1,m2,m3...mn,summary_record, m(n+1),m(n+2)...]
	  4. 恢复的时候给到的 postCompactMessages 是 [ m(n+1),m(n+2)...]
	*/
	Resume(ctx context.Context, compact *CompactRecord, postCompactMessages []*Message) (*ResumeResult, error)
}

type HistoryRolloutStore interface {
	Append(ctx context.Context, rec *HistoryRecord) error
	List(ctx context.Context, q ListQuery) ([]*HistoryRecord, error)
}

type ListOrder string

const (
	ListOrderASC  ListOrder = "asc"
	ListOrderDESC ListOrder = "desc"
)

type ListQuery struct {
	ThreadID string
	RunID    string `json:"TurnID" yaml:"turnid"`
	Order    ListOrder
	Limit    int
	// Seq cursor. Semantics depend on Order:
	// - Order DESC: return records with Seq < BeforeID (older).
	// - Order ASC: return records with Seq > AfterID (newer).
	BeforeID *int64
	AfterID  *int64
}

type ContextCompactedPayload struct {
	StrategyID string
	Before     ContextUsageSnapshot
	After      ContextUsageSnapshot
}

type ContextCompactStartedPayload struct {
	ContextUsage ContextUsageSnapshot
}
