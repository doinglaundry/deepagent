package conversation

import (
	"context"
	"fmt"
	"sync"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func (conversation *Conversation) GetContextUsage() types.ContextUsageSnapshot {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return conversation.usage.snapshot
}

func (conversation *Conversation) RecordModelUsage(_ context.Context, modelUsage *model.TokenUsage) {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	conversation.usage.recordModelUsage(modelUsage)
}

func (conversation *Conversation) GetRunUsage() types.Usage { return conversation.usage.GetRunUsage() }

func (conversation *Conversation) RestoreRunUsage(ctx context.Context, usage types.Usage) error {
	return conversation.usage.RestoreRunUsage(ctx, usage)
}

// SnapshotContext captures usage and its durable history boundary together.
func (conversation *Conversation) SnapshotContext() types.ContextSnapshot {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return types.ContextSnapshot{HistoryCursor: conversation.cursor, Usage: conversation.usage.snapshot}
}

// RestoreContext never applies a provider baseline to a different history window.
func (conversation *Conversation) RestoreContext(ctx context.Context, snapshot types.ContextSnapshot) error {
	err := validateUsageSnapshot(ctx, snapshot.Usage)
	if err != nil {
		return err
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if snapshot.HistoryCursor < 0 || snapshot.HistoryCursor > conversation.cursor {
		return fmt.Errorf("checkpoint history cursor %d exceeds durable cursor %d", snapshot.HistoryCursor, conversation.cursor)
	}
	if snapshot.HistoryCursor == conversation.cursor {
		conversation.usage.snapshot = snapshot.Usage
	}
	return nil
}

// RestoreUsage restores isolated child context whose history is in the checkpoint.
func (conversation *Conversation) RestoreUsage(ctx context.Context, snapshot types.ContextUsageSnapshot) error {
	err := validateUsageSnapshot(ctx, snapshot)
	if err != nil {
		return err
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	conversation.usage.snapshot = snapshot
	return nil
}

func validateUsageSnapshot(ctx context.Context, snapshot types.ContextUsageSnapshot) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if snapshot.ContextWindow < 0 || snapshot.CurrentTotal < 0 || snapshot.LastModelTotal < 0 || snapshot.EstimatedAfterLastModel < 0 || snapshot.LastModelPromptTokens < 0 || snapshot.LastModelCompletionTokens < 0 {
		return fmt.Errorf("invalid negative usage snapshot")
	}
	if snapshot.Source != types.ContextUsageSourceEstimated && snapshot.Source != types.ContextUsageSourceModelUsage {
		return fmt.Errorf("invalid usage source %q", snapshot.Source)
	}
	if snapshot.Source == types.ContextUsageSourceModelUsage && snapshot.CurrentTotal != snapshot.LastModelTotal+snapshot.EstimatedAfterLastModel {
		return fmt.Errorf("usage snapshot baseline mismatch")
	}
	return nil
}

// UsageTracker is owned and locked by Conversation. RunState receives snapshots.
type UsageTracker struct {
	counter  TokenCounter
	snapshot types.ContextUsageSnapshot
	runMu    sync.Mutex
	usage    types.Usage
}

// RunUsage is cumulative provider usage, independent of the context-window
// estimate. Compaction must never reset this counter.
func (usageTracker *UsageTracker) GetRunUsage() types.Usage {
	usageTracker.runMu.Lock()
	defer usageTracker.runMu.Unlock()
	return usageTracker.usage
}

func (usageTracker *UsageTracker) RestoreRunUsage(ctx context.Context, usage types.Usage) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return fmt.Errorf("invalid negative cumulative usage")
	}
	usageTracker.runMu.Lock()
	defer usageTracker.runMu.Unlock()
	usageTracker.usage = usage
	return nil
}

func (usageTracker *UsageTracker) RecordRunUsage(modelUsage *model.TokenUsage) {
	if modelUsage == nil {
		return
	}
	usageTracker.runMu.Lock()
	defer usageTracker.runMu.Unlock()
	usageTracker.usage.PromptTokens += int64(modelUsage.PromptTokens)
	usageTracker.usage.CompletionTokens += int64(modelUsage.CompletionTokens)
	total := modelUsage.TotalTokens
	if total == 0 {
		total = modelUsage.PromptTokens + modelUsage.CompletionTokens
	}
	usageTracker.usage.TotalTokens += int64(total)
}

func (usageTracker *UsageTracker) recomputeContextUsage(messages []*schema.Message) {
	contextWindow := usageTracker.snapshot.ContextWindow
	usageTracker.snapshot = types.ContextUsageSnapshot{ContextWindow: contextWindow, Source: types.ContextUsageSourceEstimated, CurrentTotal: int64(usageTracker.counter(messages))}
}

func (usageTracker *UsageTracker) addMessageUsage(message *schema.Message) {
	estimatedTokens := int64(usageTracker.counter([]*schema.Message{message}))
	if usageTracker.snapshot.Source == types.ContextUsageSourceModelUsage {
		usageTracker.snapshot.EstimatedAfterLastModel += estimatedTokens
	}
	usageTracker.snapshot.CurrentTotal += estimatedTokens
}

func (usageTracker *UsageTracker) recordModelUsage(modelUsage *model.TokenUsage) {
	if modelUsage == nil {
		return
	}
	usageTracker.RecordRunUsage(modelUsage)
	usageTracker.snapshot.Source = types.ContextUsageSourceModelUsage
	usageTracker.snapshot.LastModelPromptTokens = int64(modelUsage.PromptTokens)
	usageTracker.snapshot.LastModelCompletionTokens = int64(modelUsage.CompletionTokens)
	usageTracker.snapshot.LastModelTotal = int64(modelUsage.TotalTokens)
	if usageTracker.snapshot.LastModelTotal == 0 {
		usageTracker.snapshot.LastModelTotal = int64(modelUsage.PromptTokens + modelUsage.CompletionTokens)
	}
	usageTracker.snapshot.EstimatedAfterLastModel = 0
	usageTracker.snapshot.CurrentTotal = usageTracker.snapshot.LastModelTotal
}
