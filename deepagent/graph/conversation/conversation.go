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
	return func(c *Conversation) { c.replaceBootstrapPrompt = enabled }
}

func WithRecordID(p HistoryRecordIDProvider) Option { return func(c *Conversation) { c.recordID = p } }

func WithContextWindow(window int64) Option {
	return func(c *Conversation) { c.usage.snapshot.ContextWindow = window }
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

func New(threadID string, store HistoryRolloutStore, compactor CompactionStrategy, counter TokenCounter, opts ...Option) *Conversation {
	if counter == nil {
		counter = utils.SimpleTokenCounter
	}
	c := &Conversation{threadID: threadID, store: store, compactor: compactor, usage: &UsageTracker{counter: counter}, seen: make(map[int64]struct{})}
	c.usage.recompute(nil)
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

func (c *Conversation) AddHistory(ctx context.Context, runID string, messages ...*schema.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, message := range messages {
		contextErr := ctx.Err()
		if contextErr != nil {
			return contextErr
		}
		if message == nil {
			continue
		}
		r := c.record(ctx, runID, message, HistoryRecordMessage)
		if r.MessageID > 0 {
			_, ok := c.seen[r.MessageID]
			if ok {
				continue
			}
		}
		if c.store != nil {
			err := c.store.Append(ctx, r)
			if err != nil {
				return err
			}
		}
		// Stores may resolve a redelivery to an existing durable ID.
		if r.MessageID > 0 {
			_, ok := c.seen[r.MessageID]
			if ok {
				continue
			}
			c.seen[r.MessageID] = struct{}{}
		}
		c.messages = append(c.messages, message)
		c.version++
		c.usage.add(message)
		if r.OrderSeq() > c.cursor {
			c.cursor = r.OrderSeq()
		}
	}
	return nil
}

func (c *Conversation) History(context.Context) []*schema.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*schema.Message(nil), c.messages...)
}

func (c *Conversation) BuildRequest(ctx context.Context, prompts []*schema.Message) ([]*schema.Message, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	history := c.History(ctx)
	if c.replaceBootstrapPrompt && len(prompts) > 0 && len(history) > 0 && history[0].Role == schema.System && !strings.HasPrefix(history[0].Content, "Earlier conversation summary:") {
		history = history[1:]
	}
	return append(append([]*schema.Message(nil), prompts...), history...), nil
}

func (c *Conversation) record(ctx context.Context, runID string, m *schema.Message, kind HistoryRecordType) *HistoryRecord {
	now := time.Now()
	r := &HistoryRecord{ThreadID: c.threadID, RunID: runID, Type: kind, Message: m, UniqueKey: uuid.NewString(), CreateAt: now.Unix(), CreateAtMS: now.UnixMilli()}
	if c.recordID != nil {
		r.MessageID = c.recordID(ctx, c.threadID, runID, m)
	}
	return r
}

func (c *Conversation) ReloadHistory(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return nil
	}
	var messages []*schema.Message
	seen := make(map[int64]struct{})
	var cursor int64
	for {
		rows, err := c.store.List(ctx, ListQuery{ThreadID: c.threadID, Order: ListOrderASC, Limit: 200, AfterID: &cursor})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		previous := cursor
		for _, r := range rows {
			if r == nil {
				return fmt.Errorf("nil history record")
			}
			if r.OrderSeq() <= cursor {
				return fmt.Errorf("history sequence did not advance: %d", r.OrderSeq())
			}
			cursor = r.OrderSeq()
			if r.MessageID > 0 {
				_, ok := seen[r.MessageID]
				if ok {
					continue
				}
				seen[r.MessageID] = struct{}{}
			}
			switch r.Type {
			case HistoryRecordMessage:
				if r.Message != nil {
					messages = append(messages, r.Message)
				}
			case HistoryRecordCompact:
				messages, err = c.restoreCompact(r)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown history record type %q", r.Type)
			}
		}
		if cursor <= previous {
			return fmt.Errorf("history cursor did not advance")
		}
		if len(rows) < 200 {
			break
		}
	}
	c.messages = messages
	c.seen = seen
	c.cursor = cursor
	c.version++
	c.usage.recompute(messages)
	return nil
}
