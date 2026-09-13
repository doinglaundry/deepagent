package agentthread

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"eino-cli/deepagent/core/constant"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/utils"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// MemoryContextManager is a context manager implemented as a middleware.
// It resides within the agentthread package but conforms to the middleware.Middleware interface,
// allowing it to be injected into a DeepAgent while having access to agentthread's components like HistoryRolloutStore.
// It handles the closed loop of message persistence, recovery, and compression.
type MemoryContextManager struct {
	middleware.BaseMiddleware

	threadID string
	runID    string

	rs           HistoryRolloutStore
	strategy     CompactionStrategy // For handling context compression.
	tokenCounter TokenCounter
	tracker      *TokenUsageTracker
	onAddHistory OnAddHistory

	mu       sync.RWMutex
	messages []*schema.Message
	// historyVersion changes only when the message window changes. Model usage
	// callbacks during compaction must not invalidate the compact snapshot.
	historyVersion uint64
	// compactPending records that history has crossed the strategy threshold.
	// The actual compaction runs only at a pre-sampling boundary.
	compactPending bool

	// 可选的外部消息ID生成器
	msgIDProvider func(ctx context.Context, threadID, runID string) int64

	uniqueKeyProvider func(ctx context.Context, threadID, runID string, msg *schema.Message) string
}

// OnAddHistory 添加历史消息时的回调函数
type OnAddHistory func(ctx context.Context, RunID string, msg ...*schema.Message) ([]*schema.Message, error)

type MemoryContextManagerOption func(*memoryContextManagerConfig)

type memoryContextManagerConfig struct {
	contextWindow     int64
	modelName         string
	uniqueKeyProvider func(ctx context.Context, threadID, runID string, msg *schema.Message) string
	trackerOpts       []TokenUsageTrackerOption
}

func WithContextWindow(contextWindow int64) MemoryContextManagerOption {
	return func(cfg *memoryContextManagerConfig) {
		cfg.contextWindow = contextWindow
	}
}

func WithContextUsageModelName(modelName string) MemoryContextManagerOption {
	return func(cfg *memoryContextManagerConfig) {
		cfg.modelName = modelName
	}
}

func WithHistoryRecordUniqueKeyProvider(provider func(ctx context.Context, threadID, runID string, msg *schema.Message) string) MemoryContextManagerOption {
	return func(cfg *memoryContextManagerConfig) {
		cfg.uniqueKeyProvider = provider
	}
}

// WithTokenUsageTrackerOpts appends additional options to the internal
// TokenUsageTracker. Use this to inject tracker-level configuration
// (e.g. WithTrackerThreadID, WithTrackerStateStore) from outside.
func WithTokenUsageTrackerOpts(opts ...TokenUsageTrackerOption) MemoryContextManagerOption {
	return func(cfg *memoryContextManagerConfig) {
		cfg.trackerOpts = append(cfg.trackerOpts, opts...)
	}
}

const defaultHistoryPageSize = 200

// NewMemoryContextManager creates the default context manager.
func NewMemoryContextManager(threadID string,
	rs HistoryRolloutStore,
	strategy CompactionStrategy,
	tokenCounter TokenCounter,
	opts ...MemoryContextManagerOption) *MemoryContextManager {
	if tokenCounter == nil {
		tokenCounter = utils.SimpleTokenCounter
	}
	cfg := memoryContextManagerConfig{modelName: os.Getenv("MODEL_NAME")}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	contextWindow := cfg.contextWindow
	if contextWindow <= 0 {
		contextWindow = int64(constant.LookupModelContextWindow(context.Background(), cfg.modelName))
	}

	trackerOpts := append([]TokenUsageTrackerOption{WithTrackerThreadID(threadID)}, cfg.trackerOpts...)
	return &MemoryContextManager{
		threadID:          threadID,
		rs:                rs,
		strategy:          strategy,
		tokenCounter:      tokenCounter,
		tracker:           NewTokenUsageTracker(contextWindow, tokenCounter, trackerOpts...),
		messages:          make([]*schema.Message, 0),
		uniqueKeyProvider: cfg.uniqueKeyProvider,
	}
}

func (m *MemoryContextManager) WithOnAddHistory(onAddHistory OnAddHistory) *MemoryContextManager {
	m.onAddHistory = onAddHistory
	return m
}

func (m *MemoryContextManager) AddHistory(ctx context.Context, RunID string, msg ...*schema.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error

	if m.onAddHistory != nil {
		msg, err = m.onAddHistory(ctx, RunID, msg...)
		if err != nil {
			return err
		}
	}

	for i, cur := range msg {
		if err := m.persistMessage(ctx, RunID, cur); err != nil {
			// Add already-persisted messages to in-memory context to stay consistent
			if i > 0 {
				m.addMessageToContext(msg[:i]...)
				m.historyVersion++
			}
			return err
		}
	}

	m.addMessageToContext(msg...)
	if len(msg) > 0 {
		m.historyVersion++
	}

	if m.shouldCompactDueLocked() {
		m.compactPending = true
	}

	return nil
}

func (m *MemoryContextManager) History(ctx context.Context) []*schema.Message {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*schema.Message, len(m.messages))
	copy(out, m.messages)
	return out
}

func (m *MemoryContextManager) ContextUsage() ContextUsageSnapshot {
	return m.tracker.Current()
}

func (m *MemoryContextManager) RecordModelUsage(ctx context.Context, usage *model.TokenUsage) {
	if isCompactionContext(ctx) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tracker.RecordModelUsage(ctx, usage)
	if m.shouldCompactDueLocked() {
		m.compactPending = true
	}
}

type compactionContextKey struct{}

func withCompactionContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, compactionContextKey{}, true)
}

func isCompactionContext(ctx context.Context) bool {
	v, _ := ctx.Value(compactionContextKey{}).(bool)
	return v
}

func (m *MemoryContextManager) Compact(ctx context.Context, runID string) (*ContextCompactedPayload, error) {
	m.mu.Lock()
	if m.strategy == nil {
		m.mu.Unlock()
		return nil, nil
	}
	strategy := m.strategy
	before := m.tracker.Current()
	currentWindowCopy := copyMessages(m.messages)
	historyVersion := m.historyVersion
	limit := int64(0)
	if limiter, ok := strategy.(AutoCompactLimiter); ok {
		limit = limiter.AutoCompactTokenLimit()
	}
	m.mu.Unlock()

	start := time.Now()
	slog.InfoContext(ctx, "[MemoryContextManager] compact start",
		"thread_id", m.threadID, "turn_id", runID, "messages", len(currentWindowCopy),
		"current_total", before.CurrentTotal, "source", before.Source, "limit", limit,
	)

	compacted, err := strategy.Compact(withCompactionContext(ctx), currentWindowCopy)
	if err != nil {
		slog.ErrorContext(ctx, "[MemoryContextManager] compact failed",
			"thread_id", m.threadID, "turn_id", runID, "elapsed", time.Since(start), "error", err,
		)
		return nil, err
	}
	if compacted == nil || compacted.Compact == nil {
		slog.InfoContext(ctx, "[MemoryContextManager] compact skipped by strategy",
			"thread_id", m.threadID, "turn_id", runID, "elapsed", time.Since(start),
		)
		return nil, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.historyVersion != historyVersion {
		m.compactPending = m.compactPending || m.shouldCompactDueLocked()
		slog.WarnContext(ctx, "[MemoryContextManager] compact discarded because history changed",
			"thread_id", m.threadID, "turn_id", runID, "elapsed", time.Since(start),
			"snapshot_version", historyVersion, "current_version", m.historyVersion,
		)
		return nil, nil
	}
	if err := m.persistCompact(ctx, runID, compacted.Compact); err != nil {
		slog.ErrorContext(ctx, "[MemoryContextManager] compact persist failed",
			"thread_id", m.threadID, "turn_id", runID, "elapsed", time.Since(start), "error", err,
		)
		return nil, err
	}
	m.messages = compacted.Rebuilt
	m.tracker.Recompute(ctx, m.messages)
	m.historyVersion++
	m.compactPending = false
	payload := &ContextCompactedPayload{
		StrategyID: compacted.Compact.CompactStrategyID,
		Before:     before,
		After:      m.tracker.Current(),
	}
	rebuiltMessages := len(m.messages)
	slog.InfoContext(ctx, "[MemoryContextManager] compact done",
		"thread_id", m.threadID, "turn_id", runID, "elapsed", time.Since(start),
		"before_total", before.CurrentTotal, "after_total", payload.After.CurrentTotal,
		"rebuilt_messages", rebuiltMessages, "strategy", compacted.Compact.CompactStrategyID,
	)
	return payload, nil
}

func (m *MemoryContextManager) CompactNeeded(ctx context.Context) bool {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.strategy == nil {
		return false
	}
	if !m.compactPending && !m.shouldCompactDueLocked() {
		return false
	}
	return true
}

// Deprecated: history record ID generation belongs to HistoryRolloutStore.
// This hook remains only for compatibility with existing store implementations
// that still expect the context manager to prefill HistoryRecord.MessageID.
func (m *MemoryContextManager) SetMessageIDProvider(f func(ctx context.Context, threadID, runID string) int64) {
	m.msgIDProvider = f
}

// ReloadHistory restores the in-memory message history from the RolloutStore.
// It reads records in reverse order, stopping at the first compact record
// to delegate reconstruction to the strategy.
func (m *MemoryContextManager) ReloadHistory(ctx context.Context) error {
	if m.rs == nil {
		return nil // No rollout store configured, nothing to resume.
	}

	var rebuilt []*schema.Message
	var compactFound bool

	// A temporary list to hold messages that are *after* the compaction point.
	// We scan in DESC order and reverse at the end.
	var postCompactMessages []*schema.Message
	var compactRecord *CompactRecord

	var before *int64
	for {
		records, err := m.rs.List(ctx, ListQuery{
			ThreadID: m.threadID,
			Order:    ListOrderDESC,
			Limit:    defaultHistoryPageSize,
			BeforeID: before,
		})
		if err != nil {
			return err
		}
		if len(records) == 0 {
			break
		}

		for _, rec := range records {
			if rec == nil {
				continue
			}
			if rec.Type == HistoryRecordCompact {
				ext := rec.Ext
				if ext == nil {
					ext = &HistoryRecordExtend{}
				}
				compactRecord = &CompactRecord{
					Summary:                rec.Message,
					CompactStrategyID:      ext.CompactStrategyID,
					CompactStrategyPayload: ext.CompactStrategyPayload,
				}
				compactFound = true
				break
			}
			if rec.Type == HistoryRecordMessage && rec.Message != nil {
				postCompactMessages = append(postCompactMessages, rec.Message)
			}
		}

		if compactFound {
			break
		}

		for i := len(records) - 1; i >= 0; i-- {
			if records[i] != nil {
				seq := records[i].OrderSeq()
				before = &seq
				break
			}
		}
		if len(records) < defaultHistoryPageSize {
			break
		}
	}

	// Reverse to ASC order.
	for i, j := 0, len(postCompactMessages)-1; i < j; i, j = i+1, j-1 {
		postCompactMessages[i], postCompactMessages[j] = postCompactMessages[j], postCompactMessages[i]
	}

	if compactFound && compactRecord != nil {
		if m.strategy != nil {
			resumeRes, err := m.strategy.Resume(ctx, compactRecord, postCompactMessages)
			if err == nil && resumeRes != nil && len(resumeRes.Rebuilt) > 0 {
				rebuilt = resumeRes.Rebuilt
			} else {
				rebuilt = append([]*schema.Message{compactRecord.Summary}, postCompactMessages...)
			}
		} else {
			slog.ErrorContext(ctx, "[ResumeFromRollout] found compact but strategy is nil")
			rebuilt = append([]*schema.Message{compactRecord.Summary}, postCompactMessages...)
		}
	} else {
		rebuilt = postCompactMessages
	}

	// 4. Safely update the in-memory state.
	m.mu.Lock()
	m.messages = rebuilt
	m.tracker.Recompute(ctx, m.messages)
	m.compactPending = m.shouldCompactDueLocked()
	m.historyVersion++
	m.mu.Unlock()

	return nil
}

// persistMessage is a helper to write a MessageRecord to the RolloutStore.
func (m *MemoryContextManager) persistMessage(ctx context.Context, runID string, msg *schema.Message) error {
	if msg == nil || m.rs == nil {
		return nil
	}
	now := time.Now()
	record := &HistoryRecord{
		Type:       HistoryRecordMessage,
		ThreadID:   m.threadID,
		RunID:      runID,
		UniqueKey:  m.newHistoryRecordUniqueKey(ctx, runID, msg),
		MessageID:  m.newMessageID(ctx, runID),
		Message:    msg,
		CreateAt:   now.Unix(),
		CreateAtMS: now.UnixMilli(),
		Ext:        nil,
	}

	return m.rs.Append(ctx, record)
}

// persistCompact is a helper to write a CompactRecord to the RolloutStore.
func (m *MemoryContextManager) persistCompact(ctx context.Context, runID string, compact *CompactRecord) error {
	if compact == nil || m.rs == nil {
		return nil
	}
	now := time.Now()
	record := &HistoryRecord{
		Type:       HistoryRecordCompact,
		ThreadID:   m.threadID,
		RunID:      runID,
		UniqueKey:  m.newHistoryRecordUniqueKey(ctx, runID, compact.Summary),
		MessageID:  m.newMessageID(ctx, runID),
		Message:    compact.Summary,
		CreateAt:   now.Unix(),
		CreateAtMS: now.UnixMilli(),
		Ext: &HistoryRecordExtend{
			CompactStrategyID:      compact.CompactStrategyID,
			CompactStrategyPayload: compact.CompactStrategyPayload,
		},
	}

	return m.rs.Append(ctx, record)
}

func (m *MemoryContextManager) addMessageToContext(msg ...*schema.Message) {
	m.messages = append(m.messages, msg...)
	m.tracker.AddLocalMessages(msg)
}

func (m *MemoryContextManager) shouldCompactDueLocked() bool {
	if m.strategy == nil {
		return false
	}
	limiter, ok := m.strategy.(AutoCompactLimiter)
	if !ok {
		return true
	}
	return m.tracker.ShouldCompact(limiter.AutoCompactTokenLimit())
}

func (m *MemoryContextManager) newMessageID(ctx context.Context, runID string) int64 {
	if m.msgIDProvider != nil {
		return m.msgIDProvider(ctx, m.threadID, runID)
	}
	return 0
}

func (m *MemoryContextManager) newHistoryRecordUniqueKey(ctx context.Context, runID string, msg *schema.Message) string {
	if m.uniqueKeyProvider != nil {
		if key := m.uniqueKeyProvider(ctx, m.threadID, runID, msg); key != "" {
			return key
		}
	}
	return uuid.NewString()
}
