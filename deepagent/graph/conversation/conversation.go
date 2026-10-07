package conversation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"eino-cli/deepagent/graph/types"
	"eino-cli/deepagent/utils"
	"github.com/cloudwego/eino/schema"
)

type Option func(*Conversation)

func WithRecordID(provider HistoryRecordIDProvider) Option {
	return func(conversation *Conversation) { conversation.recordID = provider }
}
func WithContextWindow(window int64) Option {
	return func(conversation *Conversation) { conversation.contextUsage.ContextWindow = window }
}

// Conversation publishes changes only after durable writes succeed.
type Conversation struct {
	mu              sync.Mutex
	threadID        string
	messages        []*schema.Message
	seenMessageIDs  map[int64]struct{}
	historySequence int64
	version         uint64
	store           HistoryStore
	compactor       CompactionStrategy
	recordID        HistoryRecordIDProvider
	tokenCounter    TokenCounter
	contextUsage    types.ContextUsageSnapshot
	runUsage        types.Usage
}

func New(threadID string, store HistoryStore, compactor CompactionStrategy, counter TokenCounter, opts ...Option) *Conversation {
	if counter == nil {
		counter = utils.SimpleTokenCounter
	}
	conversation := &Conversation{threadID: threadID, store: store, compactor: compactor, tokenCounter: counter, seenMessageIDs: make(map[int64]struct{})}
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
		record, err := conversation.buildHistoryRecord(ctx, runID, message, HistoryRecordMessage)
		if err != nil {
			return err
		}
		_, exists := conversation.seenMessageIDs[record.MessageID]
		if record.MessageID > 0 && exists {
			continue
		}
		if conversation.store != nil {
			err = conversation.store.Append(ctx, record)
			if err != nil {
				return err
			}
		}
		if record.MessageID > 0 {
			conversation.seenMessageIDs[record.MessageID] = struct{}{}
		}
		conversation.messages = append(conversation.messages, message)
		conversation.version++
		conversation.addMessageUsage(message)
		conversation.historySequence = max(conversation.historySequence, record.Seq)
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
func (conversation *Conversation) buildHistoryRecord(ctx context.Context, runID string, message *schema.Message, kind HistoryRecordType) (*HistoryRecord, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	record := &HistoryRecord{ThreadID: conversation.threadID, RunID: runID, Type: kind, Message: message, CreatedAt: time.Now().Unix()}
	if conversation.recordID != nil {
		record.MessageID, err = conversation.recordID(ctx, conversation.threadID, runID, message)
		if err != nil {
			return nil, err
		}
	}
	return record, nil
}
func (conversation *Conversation) ReloadHistory(ctx context.Context) error {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if conversation.store == nil {
		return nil
	}
	var messages []*schema.Message
	seenMessageIDs := make(map[int64]struct{})
	var sequence int64
	for {
		records, err := conversation.store.LoadAfter(ctx, conversation.threadID, sequence, 200)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record == nil || record.Seq <= sequence {
				return fmt.Errorf("history sequence must advance past %d", sequence)
			}
			sequence = record.Seq
			_, exists := seenMessageIDs[record.MessageID]
			if record.MessageID > 0 && exists {
				continue
			}
			if record.MessageID > 0 {
				seenMessageIDs[record.MessageID] = struct{}{}
			}
			switch record.Type {
			case HistoryRecordMessage:
				if record.Message != nil {
					messages = append(messages, record.Message)
				}
			case HistoryRecordCompact:
				if len(record.CompactedMessages) == 0 || record.CompactedMessages[0] == nil || record.CompactedMessages[0].Role != schema.System {
					return fmt.Errorf("compact record requires a summary and rebuilt context")
				}
				messages = append([]*schema.Message(nil), record.CompactedMessages...)
			default:
				return fmt.Errorf("unknown history record type %q", record.Type)
			}
		}
		if len(records) < 200 {
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
