package conversation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"eino-cli/deepagent/utils"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type Option func(*Conversation)

// WithBootstrapPromptReplacement supports histories written by the old Worker,
// which stored its startup prompt as the first system message. Summary messages
// keep their historical prefix and are always retained.
func WithBootstrapPromptReplacement(enabled bool) Option {
	return func(conversation *Conversation) { conversation.replaceBootstrapPrompt = enabled }
}

func WithRecordID(recordIDProvider HistoryRecordIDProvider) Option {
	return func(conversation *Conversation) { conversation.recordID = recordIDProvider }
}

func WithContextWindow(window int64) Option {
	return func(conversation *Conversation) { conversation.usage.snapshot.ContextWindow = window }
}

// Conversation serializes durable writes and in-memory publication. Compression
// is deliberately computed outside this lock and committed against version.
type Conversation struct {
	replaceBootstrapPrompt bool
	mu                     sync.Mutex
	version                uint64
	threadID               string
	messages               []*schema.Message
	store                  HistoryRolloutStore
	compactor              CompactionStrategy
	usage                  *UsageTracker
	recordID               HistoryRecordIDProvider
	seen                   map[int64]struct{}
	cursor                 int64
}

func New(threadID string, store HistoryRolloutStore, compactor CompactionStrategy, tokenCounter TokenCounter, opts ...Option) *Conversation {
	if tokenCounter == nil {
		tokenCounter = utils.SimpleTokenCounter
	}
	conversation := &Conversation{threadID: threadID, store: store, compactor: compactor, usage: &UsageTracker{counter: tokenCounter}, seen: make(map[int64]struct{})}
	conversation.usage.recomputeContextUsage(nil)
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
		contextErr := ctx.Err()
		if contextErr != nil {
			return contextErr
		}
		if message == nil {
			continue
		}
		historyRecord := conversation.buildHistoryRecord(ctx, runID, message, HistoryRecordMessage)
		if historyRecord.MessageID > 0 {
			_, ok := conversation.seen[historyRecord.MessageID]
			if ok {
				continue
			}
		}
		if conversation.store != nil {
			err := conversation.store.Append(ctx, historyRecord)
			if err != nil {
				return err
			}
		}
		// Stores may resolve a redelivery to an existing durable ID.
		if historyRecord.MessageID > 0 {
			_, ok := conversation.seen[historyRecord.MessageID]
			if ok {
				continue
			}
			conversation.seen[historyRecord.MessageID] = struct{}{}
		}
		conversation.messages = append(conversation.messages, message)
		conversation.version++
		conversation.usage.addMessageUsage(message)
		if historyRecord.GetOrderSequence() > conversation.cursor {
			conversation.cursor = historyRecord.GetOrderSequence()
		}
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
	history := conversation.GetHistory(ctx)
	if conversation.replaceBootstrapPrompt && len(prompts) > 0 && len(history) > 0 && history[0].Role == schema.System && !strings.HasPrefix(history[0].Content, "Earlier conversation summary:") {
		history = history[1:]
	}
	return append(append([]*schema.Message(nil), prompts...), history...), nil
}

func (conversation *Conversation) buildHistoryRecord(ctx context.Context, runID string, message *schema.Message, kind HistoryRecordType) *HistoryRecord {
	now := time.Now()
	historyRecord := &HistoryRecord{ThreadID: conversation.threadID, RunID: runID, Type: kind, Message: message, UniqueKey: uuid.NewString(), CreateAt: now.Unix(), CreateAtMS: now.UnixMilli()}
	if conversation.recordID != nil {
		historyRecord.MessageID = conversation.recordID(ctx, conversation.threadID, runID, message)
	}
	return historyRecord
}

func (conversation *Conversation) ReloadHistory(ctx context.Context) error {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if conversation.store == nil {
		return nil
	}
	var messages []*schema.Message
	seenMessageIDs := make(map[int64]struct{})
	var cursor int64
	for {
		rows, err := conversation.store.List(ctx, ListQuery{ThreadID: conversation.threadID, Order: ListOrderASC, Limit: 200, AfterID: &cursor})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		previousCursor := cursor
		for _, historyRecord := range rows {
			if historyRecord == nil {
				return fmt.Errorf("nil history record")
			}
			if historyRecord.GetOrderSequence() <= cursor {
				return fmt.Errorf("history sequence did not advance: %d", historyRecord.GetOrderSequence())
			}
			cursor = historyRecord.GetOrderSequence()
			if historyRecord.MessageID > 0 {
				_, ok := seenMessageIDs[historyRecord.MessageID]
				if ok {
					continue
				}
				seenMessageIDs[historyRecord.MessageID] = struct{}{}
			}
			switch historyRecord.Type {
			case HistoryRecordMessage:
				if historyRecord.Message != nil {
					messages = append(messages, historyRecord.Message)
				}
			case HistoryRecordCompact:
				messages, err = conversation.restoreCompact(historyRecord)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown history record type %q", historyRecord.Type)
			}
		}
		if cursor <= previousCursor {
			return fmt.Errorf("history cursor did not advance")
		}
		if len(rows) < 200 {
			break
		}
	}
	conversation.messages = messages
	conversation.seen = seenMessageIDs
	conversation.cursor = cursor
	conversation.version++
	conversation.usage.recomputeContextUsage(messages)
	return nil
}
