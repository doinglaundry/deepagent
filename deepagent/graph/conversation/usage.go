package conversation

import (
	"context"
	"fmt"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/components/model"
)

func (conversation *Conversation) GetContextUsage() types.ContextTokenUsage {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return conversation.contextTokenUsage
}
func (conversation *Conversation) GetRunUsage() types.Usage {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return conversation.runUsage
}
func (conversation *Conversation) RecordModelUsage(_ context.Context, modelUsage *model.TokenUsage) {
	if modelUsage == nil {
		return
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	contextTokenUsage := types.ContextTokenUsage{
		MaxContextTokens: conversation.contextTokenUsage.MaxContextTokens,
		TotalTokens:      int64(modelUsage.TotalTokens),
		PromptTokens:     int64(modelUsage.PromptTokens),
		CompletionTokens: int64(modelUsage.CompletionTokens),
	}
	if contextTokenUsage.TotalTokens == 0 {
		contextTokenUsage.TotalTokens = contextTokenUsage.PromptTokens + contextTokenUsage.CompletionTokens
	}
	conversation.runUsage.PromptTokens += contextTokenUsage.PromptTokens
	conversation.runUsage.CompletionTokens += contextTokenUsage.CompletionTokens
	conversation.runUsage.TotalTokens += contextTokenUsage.TotalTokens
	conversation.contextTokenUsage = contextTokenUsage
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

// SnapshotContext 在同一把锁下读取历史序号和对应的用量。
func (conversation *Conversation) SnapshotContext() (int64, types.ContextTokenUsage) {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return conversation.historySequence, conversation.contextTokenUsage
}
func (conversation *Conversation) RestoreContext(ctx context.Context, historySeq int64, contextTokenUsage types.ContextTokenUsage) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if contextTokenUsage.MaxContextTokens < 0 ||
		contextTokenUsage.TotalTokens < 0 ||
		contextTokenUsage.PromptTokens < 0 ||
		contextTokenUsage.CompletionTokens < 0 {
		return fmt.Errorf("invalid negative context token usage")
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if historySeq < 0 || historySeq > conversation.historySequence {
		return fmt.Errorf("checkpoint history cursor %d exceeds durable cursor %d", historySeq, conversation.historySequence)
	}
	if historySeq == conversation.historySequence {
		conversation.contextTokenUsage = contextTokenUsage
	}
	return nil
}
func (conversation *Conversation) recomputeContextUsage() {
	conversation.contextTokenUsage = types.ContextTokenUsage{
		MaxContextTokens: conversation.contextTokenUsage.MaxContextTokens,
		TotalTokens:      int64(conversation.countTokenFunc(conversation.messages)),
	}
}
func (conversation *Conversation) addMessageUsage(message *messagepkg.Message) {
	messageTokens := conversation.countTokenFunc([]*messagepkg.Message{message})
	conversation.contextTokenUsage.TotalTokens += int64(messageTokens)
}
