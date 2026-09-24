package conversation

import (
	"context"
	"fmt"
	"sync"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// UsageTracker is owned and locked by Conversation. RunState receives snapshots.
type UsageTracker struct {
	counter  TokenCounter
	snapshot ContextUsageSnapshot
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
	if err := ctx.Err(); err != nil {
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
	u.snapshot = ContextUsageSnapshot{ContextWindow: window, Source: ContextUsageSourceEstimated, CurrentTotal: int64(u.counter(messages))}
}
func (u *UsageTracker) add(message *schema.Message) {
	delta := int64(u.counter([]*schema.Message{message}))
	if u.snapshot.Source == ContextUsageSourceModelUsage {
		u.snapshot.EstimatedAfterLastModel += delta
	}
	u.snapshot.CurrentTotal += delta
}
func (u *UsageTracker) record(usage *model.TokenUsage) {
	if usage == nil {
		return
	}
	u.RecordRunUsage(usage)
	u.snapshot.Source = ContextUsageSourceModelUsage
	u.snapshot.LastModelPromptTokens = int64(usage.PromptTokens)
	u.snapshot.LastModelCompletionTokens = int64(usage.CompletionTokens)
	u.snapshot.LastModelTotal = int64(usage.TotalTokens)
	if u.snapshot.LastModelTotal == 0 {
		u.snapshot.LastModelTotal = int64(usage.PromptTokens + usage.CompletionTokens)
	}
	u.snapshot.EstimatedAfterLastModel = 0
	u.snapshot.CurrentTotal = u.snapshot.LastModelTotal
}
