package agentthread

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"eino-cli/deepagent/utils"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

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

// TokenUsageSnapshot is kept as a compatibility alias. Prefer
// ContextUsageSnapshot in new code.
type TokenUsageSnapshot = ContextUsageSnapshot

// TokenUsageTrackerOption configures optional behavior for TokenUsageTracker.
type TokenUsageTrackerOption func(*TokenUsageTracker)

// WithTrackerThreadID sets the thread identity used when persisting context state.
func WithTrackerThreadID(id string) TokenUsageTrackerOption {
	return func(t *TokenUsageTracker) {
		t.threadID = id
	}
}

// WithTrackerStateStore registers an optional store for best-effort persistence
// of usage snapshots. When set, the tracker persists state after RecordModelUsage
// and Recompute. The store is never called under lock.
func WithTrackerStateStore(store ContextStateStore) TokenUsageTrackerOption {
	return func(t *TokenUsageTracker) {
		t.stateStore = store
	}
}

type TokenUsageTracker struct {
	mu sync.RWMutex

	// contextWindow is the known maximum model context window. It is reported
	// for observability; compact decisions are based on CurrentTotal and the
	// strategy limit.
	contextWindow int64
	// counter estimates tokens for local history when provider usage is not yet
	// available, and for local messages appended after the last model response.
	counter TokenCounter

	// hasModelUsage indicates whether lastModelTotal is authoritative. When
	// false, estimatedTotal is the active value.
	hasModelUsage bool
	// lastModelTotal is the provider-reported total_tokens from the last
	// successful model response.
	lastModelTotal int64
	// lastModelPromptTokens and lastModelCompleteToken are carried for snapshots
	// so callers can inspect the last response's usage breakdown.
	lastModelPromptTokens  int64
	lastModelCompleteToken int64
	// estimatedAfterModel tracks local user/tool messages appended after the
	// last model response. Assistant messages are skipped because the provider
	// already counted them in lastModelTotal.
	estimatedAfterModel int64
	// estimatedTotal is used before a provider usage baseline exists and after a
	// recompute caused by reload or compaction.
	estimatedTotal int64

	// threadID identifies the thread for state persistence.
	threadID string
	// stateStore is an optional external store for persisting usage snapshots.
	// When nil, no persistence occurs.
	stateStore ContextStateStore
}

func NewTokenUsageTracker(contextWindow int64, counter TokenCounter, opts ...TokenUsageTrackerOption) *TokenUsageTracker {
	if counter == nil {
		counter = utils.SimpleTokenCounter
	}
	t := &TokenUsageTracker{
		contextWindow: contextWindow,
		counter:       counter,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	return t
}

func (t *TokenUsageTracker) SetContextWindow(contextWindow int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.contextWindow = contextWindow
}

func (t *TokenUsageTracker) AddLocalMessages(messages []*schema.Message) {
	if t == nil || len(messages) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.hasModelUsage {
		t.estimatedTotal += int64(t.counter(messages))
		return
	}

	t.estimatedAfterModel += int64(t.counter(localMessagesAfterLastModel(messages)))
}

func (t *TokenUsageTracker) RecordModelUsage(ctx context.Context, usage *model.TokenUsage) {
	if t == nil || usage == nil {
		return
	}
	func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.hasModelUsage = true
		t.lastModelTotal = int64(usage.TotalTokens)
		t.lastModelPromptTokens = int64(usage.PromptTokens)
		t.lastModelCompleteToken = int64(usage.CompletionTokens)
		t.estimatedAfterModel = 0
		if t.lastModelTotal > t.estimatedTotal {
			t.estimatedTotal = t.lastModelTotal
		}
	}()

	t.saveStateBestEffort(ctx)
}

func (t *TokenUsageTracker) Recompute(ctx context.Context, messages []*schema.Message) {
	if t == nil {
		return
	}
	func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.hasModelUsage = false
		t.lastModelTotal = 0
		t.lastModelPromptTokens = 0
		t.lastModelCompleteToken = 0
		t.estimatedAfterModel = 0
		t.estimatedTotal = int64(t.counter(messages))
	}()

	t.saveStateBestEffort(ctx)
}

func (t *TokenUsageTracker) Current() ContextUsageSnapshot {
	if t == nil {
		return ContextUsageSnapshot{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.hasModelUsage {
		total := t.lastModelTotal + t.estimatedAfterModel
		source := ContextUsageSourceModelUsage
		if t.estimatedTotal > total {
			total = t.estimatedTotal
			source = ContextUsageSourceEstimated
		}
		return t.snapshotLocked(total, source)
	}
	return t.snapshotLocked(t.estimatedTotal, ContextUsageSourceEstimated)
}

func (t *TokenUsageTracker) ShouldCompact(limit int64) bool {
	if t == nil || limit <= 0 {
		return false
	}
	return t.Current().CurrentTotal >= limit
}

func (t *TokenUsageTracker) snapshotLocked(total int64, source ContextUsageSource) ContextUsageSnapshot {
	return ContextUsageSnapshot{
		ContextWindow:             t.contextWindow,
		LastModelTotal:            t.lastModelTotal,
		EstimatedAfterLastModel:   t.estimatedAfterModel,
		CurrentTotal:              total,
		Source:                    source,
		LastModelPromptTokens:     t.lastModelPromptTokens,
		LastModelCompletionTokens: t.lastModelCompleteToken,
	}
}

// saveStateBestEffort persists the current usage snapshot to the registered
// state store. Called outside of the tracker's lock. Failures are logged but
// do not propagate — state persistence is best-effort.
func (t *TokenUsageTracker) saveStateBestEffort(ctx context.Context) {
	if t.stateStore == nil {
		return
	}
	state := ContextState{
		ThreadID:    t.threadID,
		Usage:       t.Current(),
		UpdatedAtMS: time.Now().UnixMilli(),
	}
	if err := t.stateStore.Save(ctx, state); err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("[TokenUsageTracker] save context state failed: thread_id=%s err=%v", t.threadID, err))
	}
}

func localMessagesAfterLastModel(messages []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil || msg.Role == schema.Assistant {
			continue
		}
		out = append(out, msg)
	}
	return out
}
