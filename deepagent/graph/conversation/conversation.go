package conversation

import (
	"context"
	"fmt"
	"sync"
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/graph/types"
	"eino-cli/deepagent/utils"
	"github.com/cloudwego/eino/schema"
)

type Option func(*Conversation)

func WithEntryID(provider ConversationEntryIDProvider) Option {
	return func(conversation *Conversation) { conversation.entryID = provider }
}
func WithContextWindow(window int64) Option {
	return func(conversation *Conversation) { conversation.contextUsage.ContextWindow = window }
}

// Conversation publishes changes only after durable writes succeed.
type Conversation struct {
	mu                     sync.Mutex
	threadID               string
	messages               []*schema.Message
	seenMessageIDs         map[int64]struct{}
	historySequence        int64
	version                uint64
	conversationRepository ConversationRepository
	compactor              CompactionStrategy
	entryID                ConversationEntryIDProvider
	tokenCounter           TokenCounter
	contextUsage           types.ContextUsageSnapshot
	runUsage               types.Usage
}

func New(threadID string, conversationRepository ConversationRepository, compactor CompactionStrategy, counter TokenCounter, opts ...Option) *Conversation {
	if counter == nil {
		counter = utils.SimpleTokenCounter
	}
	conversation := &Conversation{threadID: threadID, conversationRepository: conversationRepository, compactor: compactor, tokenCounter: counter, seenMessageIDs: make(map[int64]struct{})}
	conversation.recomputeContextUsage()
	for _, opt := range opts {
		if opt != nil {
			opt(conversation)
		}
	}
	return conversation
}
func (conversation *Conversation) AddHistory(ctx context.Context, runID string, messages ...*schema.Message) error {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	for _, message := range messages {
		if message == nil {
			continue
		}
		conversationEntry, err := conversation.buildConversationEntry(ctx, runID, message, dalmodel.ConversationEntryMessage)
		if err != nil {
			return err
		}
		_, exists := conversation.seenMessageIDs[conversationEntry.MessageID]
		if conversationEntry.MessageID > 0 && exists {
			continue
		}
		if conversation.conversationRepository != nil {
			err = conversation.conversationRepository.Append(ctx, conversationEntry)
			if err != nil {
				return err
			}
		}
		if conversationEntry.MessageID > 0 {
			conversation.seenMessageIDs[conversationEntry.MessageID] = struct{}{}
		}
		conversation.messages = append(conversation.messages, message)
		conversation.version++
		conversation.addMessageUsage(message)
		conversation.historySequence = max(conversation.historySequence, conversationEntry.Seq)
	}
	return nil
}
func (conversation *Conversation) GetHistory(context.Context) []*schema.Message {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return append([]*schema.Message(nil), conversation.messages...)
}
func (conversation *Conversation) BuildRequest(ctx context.Context, prompts []*schema.Message) ([]*schema.Message, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	request := make([]*schema.Message, 0, len(prompts)+len(conversation.messages))
	request = append(request, prompts...)
	return append(request, conversation.messages...), nil
}
func (conversation *Conversation) buildConversationEntry(ctx context.Context, runID string, message *schema.Message, kind dalmodel.ConversationEntryType) (*dalmodel.ConversationEntry, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	conversationEntry := &dalmodel.ConversationEntry{ThreadID: conversation.threadID, RunID: runID, Type: kind, Message: message, CreatedAt: time.Now().Unix()}
	if conversation.entryID != nil {
		conversationEntry.MessageID, err = conversation.entryID(ctx, conversation.threadID, runID, message)
		if err != nil {
			return nil, err
		}
	}
	return conversationEntry, nil
}
func (conversation *Conversation) ReloadHistory(ctx context.Context) error {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if conversation.conversationRepository == nil {
		return nil
	}
	var messages []*schema.Message
	seenMessageIDs := make(map[int64]struct{})
	var sequence int64
	for {
		conversationEntries, err := conversation.conversationRepository.LoadAfter(ctx, conversation.threadID, sequence, 200)
		if err != nil {
			return err
		}
		for _, conversationEntry := range conversationEntries {
			if conversationEntry == nil || conversationEntry.Seq <= sequence {
				return fmt.Errorf("history sequence must advance past %d", sequence)
			}
			sequence = conversationEntry.Seq
			_, exists := seenMessageIDs[conversationEntry.MessageID]
			if conversationEntry.MessageID > 0 && exists {
				continue
			}
			if conversationEntry.MessageID > 0 {
				seenMessageIDs[conversationEntry.MessageID] = struct{}{}
			}
			switch conversationEntry.Type {
			case dalmodel.ConversationEntryMessage:
				if conversationEntry.Message != nil {
					messages = append(messages, conversationEntry.Message)
				}
			case dalmodel.ConversationEntryCompact:
				if len(conversationEntry.CompactedMessages) == 0 || conversationEntry.CompactedMessages[0] == nil || conversationEntry.CompactedMessages[0].Role != schema.System {
					return fmt.Errorf("compact record requires a summary and rebuilt context")
				}
				messages = append([]*schema.Message(nil), conversationEntry.CompactedMessages...)
			default:
				return fmt.Errorf("unknown history record type %q", conversationEntry.Type)
			}
		}
		if len(conversationEntries) < 200 {
			break
		}
	}
	conversation.messages = messages
	conversation.seenMessageIDs = seenMessageIDs
	conversation.historySequence = sequence
	conversation.version++
	conversation.recomputeContextUsage()
	return nil
}
