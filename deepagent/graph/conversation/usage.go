package conversation

import (
	"context"
	"fmt"
	"sync"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func (c *Conversation) ContextUsage() types.ContextUsageSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage.snapshot
}

func (c *Conversation) RecordModelUsage(_ context.Context, usage *model.TokenUsage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usage.record(usage)
}

func (c *Conversation) RunUsage() types.Usage { return c.usage.RunUsage() }

func (c *Conversation) RestoreRunUsage(ctx context.Context, usage types.Usage) error {
	return c.usage.RestoreRunUsage(ctx, usage)
}

// SnapshotContext captures usage and its durable history boundary together.
func (c *Conversation) SnapshotContext() types.ContextSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return types.ContextSnapshot{HistoryCursor: c.cursor, Usage: c.usage.snapshot}
}

// RestoreContext never applies a provider baseline to a different history window.
func (c *Conversation) RestoreContext(ctx context.Context, snapshot types.ContextSnapshot) error {
	err := validateUsageSnapshot(ctx, snapshot.Usage)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if snapshot.HistoryCursor < 0 || snapshot.HistoryCursor > c.cursor {
		return fmt.Errorf("checkpoint history cursor %d exceeds durable cursor %d", snapshot.HistoryCursor, c.cursor)
	}
	if snapshot.HistoryCursor == c.cursor {
		c.usage.snapshot = snapshot.Usage
	}
	return nil
}

// RestoreUsage restores isolated child context whose history is in the checkpoint.
func (c *Conversation) RestoreUsage(ctx context.Context, snapshot types.ContextUsageSnapshot) error {
	err := validateUsageSnapshot(ctx, snapshot)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usage.snapshot = snapshot
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
	run      types.Usage
}

// RunUsage is cumulative provider usage, independent of the context-window
// estimate. Compaction must never reset this counter.
func (u *UsageTracker) RunUsage() types.Usage {
	u.runMu.Lock()
	defer u.runMu.Unlock()
	return u.run
}

func (u *UsageTracker) RestoreRunUsage(ctx context.Context, usage types.Usage) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return fmt.Errorf("invalid negative cumulative usage")
	}
	u.runMu.Lock()
	defer u.runMu.Unlock()
	u.run = usage
	return nil
}

func (u *UsageTracker) RecordRunUsage(usage *model.TokenUsage) {
	if usage == nil {
		return
	}
	u.runMu.Lock()
	defer u.runMu.Unlock()
	u.run.PromptTokens += int64(usage.PromptTokens)
	u.run.CompletionTokens += int64(usage.CompletionTokens)
	total := usage.TotalTokens
	if total == 0 {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	u.run.TotalTokens += int64(total)
}

func (u *UsageTracker) recompute(messages []*schema.Message) {
	window := u.snapshot.ContextWindow
	u.snapshot = types.ContextUsageSnapshot{ContextWindow: window, Source: types.ContextUsageSourceEstimated, CurrentTotal: int64(u.counter(messages))}
}

func (u *UsageTracker) add(message *schema.Message) {
	delta := int64(u.counter([]*schema.Message{message}))
	if u.snapshot.Source == types.ContextUsageSourceModelUsage {
		u.snapshot.EstimatedAfterLastModel += delta
	}
	u.snapshot.CurrentTotal += delta
}

func (u *UsageTracker) record(usage *model.TokenUsage) {
	if usage == nil {
		return
	}
	u.RecordRunUsage(usage)
	u.snapshot.Source = types.ContextUsageSourceModelUsage
	u.snapshot.LastModelPromptTokens = int64(usage.PromptTokens)
	u.snapshot.LastModelCompletionTokens = int64(usage.CompletionTokens)
	u.snapshot.LastModelTotal = int64(usage.TotalTokens)
	if u.snapshot.LastModelTotal == 0 {
		u.snapshot.LastModelTotal = int64(usage.PromptTokens + usage.CompletionTokens)
	}
	u.snapshot.EstimatedAfterLastModel = 0
	u.snapshot.CurrentTotal = u.snapshot.LastModelTotal
}
