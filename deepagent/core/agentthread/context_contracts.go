package agentthread

import (
	"context"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type TokenCounter func(messages []*schema.Message) int

// ContextManager owns the model-visible conversation history for one thread.
//
// Boundary:
//   - Runner/middleware call AddHistory and History to build model requests.
//   - Runner calls RecordModelUsage after successful model responses so compact
//     decisions can prefer provider-reported token usage over estimates.
//   - ContextManager owns compaction, but callers decide the boundary. Automatic
//     compaction should run before sampling. Manual compaction should run only
//     while the thread is idle.
//   - CompactionStrategy decides how to rebuild history.
//
// Implementations must be safe for the callback paths used during agent execution.
type ContextManager interface {
	// ReloadHistory restores the in-memory history from the backing store.
	// It is called during thread initialization before runs are started.
	ReloadHistory(ctx context.Context) error
	// AddHistory appends model-visible messages for a run.
	// Implementations may persist the messages and update local token estimates.
	// The input order must be preserved.
	AddHistory(ctx context.Context, RunID string, msg ...*schema.Message) error
	// History returns the current model-visible history in chronological order.
	// Callers must treat the returned slice as read-only.
	History(ctx context.Context) []*schema.Message
	// ContextUsage returns the current model context-window usage state.
	// CurrentTotal is the value used for compact triggering.
	ContextUsage() ContextUsageSnapshot
	// RecordModelUsage records token usage returned by the model provider for
	// the latest successful model response. This should reset any local delta
	// accumulated since the previous model response.
	RecordModelUsage(ctx context.Context, usage *model.TokenUsage)
	// Compact forces a compaction attempt at an explicit idle boundary.
	Compact(ctx context.Context, runID string) (*ContextCompactedPayload, error)
	// CompactNeeded reports whether automatic compaction should run before the
	// next model sampling boundary.
	CompactNeeded(ctx context.Context) bool
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
