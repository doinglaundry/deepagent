package conversation

import (
	"context"
	"fmt"

	"eino-cli/deepagent/graph/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func (conversation *Conversation) GetContextUsage() types.ContextUsageSnapshot {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return conversation.contextUsage
}
func (conversation *Conversation) GetRunUsage() types.Usage {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return conversation.runUsage
}
func (conversation *Conversation) RecordModelUsage(_ context.Context, usage *model.TokenUsage) {
	if usage == nil {
		return
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	total := int64(usage.TotalTokens)
	if total == 0 {
		total = int64(usage.PromptTokens + usage.CompletionTokens)
	}
	conversation.runUsage.PromptTokens += int64(usage.PromptTokens)
	conversation.runUsage.CompletionTokens += int64(usage.CompletionTokens)
	conversation.runUsage.TotalTokens += total
	conversation.contextUsage = types.ContextUsageSnapshot{
		ContextWindow: conversation.contextUsage.ContextWindow,
		Source:        types.ContextUsageSourceModelUsage, CurrentTotal: total, LastModelTotal: total,
		LastModelPromptTokens: int64(usage.PromptTokens), LastModelCompletionTokens: int64(usage.CompletionTokens),
	}
}
func (conversation *Conversation) RestoreRunUsage(ctx context.Context, usage types.Usage) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return fmt.Errorf("invalid negative cumulative usage")
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	conversation.runUsage = usage
	return nil
}

// Checkpoints bind usage to the exact durable history boundary it describes.
func (conversation *Conversation) SnapshotContext() types.ContextSnapshot {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return types.ContextSnapshot{HistoryCursor: conversation.historySequence, Usage: conversation.contextUsage}
}
func (conversation *Conversation) RestoreContext(ctx context.Context, snapshot types.ContextSnapshot) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	usage := snapshot.Usage
	if usage.ContextWindow < 0 || usage.CurrentTotal < 0 || usage.LastModelTotal < 0 || usage.EstimatedAfterLastModel < 0 || usage.LastModelPromptTokens < 0 || usage.LastModelCompletionTokens < 0 {
		return fmt.Errorf("invalid negative usage snapshot")
	}
	if usage.Source != types.ContextUsageSourceEstimated && usage.Source != types.ContextUsageSourceModelUsage {
		return fmt.Errorf("invalid usage source %q", usage.Source)
	}
	if usage.Source == types.ContextUsageSourceModelUsage && usage.CurrentTotal != usage.LastModelTotal+usage.EstimatedAfterLastModel {
		return fmt.Errorf("usage snapshot baseline mismatch")
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if snapshot.HistoryCursor < 0 || snapshot.HistoryCursor > conversation.historySequence {
		return fmt.Errorf("checkpoint history cursor %d exceeds durable cursor %d", snapshot.HistoryCursor, conversation.historySequence)
	}
	if snapshot.HistoryCursor == conversation.historySequence {
		conversation.contextUsage = usage
	}
	return nil
}
func (conversation *Conversation) recomputeContextUsage() {
	conversation.contextUsage = types.ContextUsageSnapshot{ContextWindow: conversation.contextUsage.ContextWindow,
		Source: types.ContextUsageSourceEstimated, CurrentTotal: int64(conversation.tokenCounter(conversation.messages))}
}
func (conversation *Conversation) addMessageUsage(message *schema.Message) {
	tokens := int64(conversation.tokenCounter([]*schema.Message{message}))
	if conversation.contextUsage.Source == types.ContextUsageSourceModelUsage {
		conversation.contextUsage.EstimatedAfterLastModel += tokens
	}
	conversation.contextUsage.CurrentTotal += tokens
}
