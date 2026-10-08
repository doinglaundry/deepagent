package conversation

import (
	"context"
	"sync"
	"time"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
	"eino-cli/deepagent/utils"
)

type Option func(*Conversation)

func WithMessageID(provider MessageIDProvider) Option {
	return func(conversation *Conversation) { conversation.messageID = provider }
}
func WithContextWindow(window int64) Option {
	return func(conversation *Conversation) { conversation.contextUsage.ContextWindow = window }
}

// Conversation publishes changes only after durable writes succeed.
type Conversation struct {
	mu                     sync.Mutex
	threadID               string
	messages               []*messagepkg.Message
	seenMessageIDs         map[string]struct{}
	historySequence        int64
	conversationRepository ConversationRepository
	compactor              CompactionStrategy
	messageID              MessageIDProvider
	tokenCounter           TokenCounter
	contextUsage           types.ContextUsageSnapshot
	runUsage               types.Usage
}

func New(threadID string, conversationRepository ConversationRepository, compactor CompactionStrategy, counter TokenCounter, opts ...Option) *Conversation {
	if counter == nil {
		counter = utils.SimpleTokenCounter
	}
	conversation := &Conversation{threadID: threadID, conversationRepository: conversationRepository, compactor: compactor, tokenCounter: counter, seenMessageIDs: make(map[string]struct{})}
	conversation.recomputeContextUsage()
	for _, opt := range opts {
		if opt != nil {
			opt(conversation)
		}
	}
	return conversation
}
func (conversation *Conversation) AddHistory(ctx context.Context, runID string, messages ...*messagepkg.Message) error {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	for _, message := range messages {
		if message == nil {
			continue
		}
		// 输入也会被 RunHandle 并发读取，不能在原对象上补写持久化元数据。
		copy := *message
		message = &copy
		err := conversation.initializeMessage(ctx, runID, message)
		if err != nil {
			return err
		}
		_, exists := conversation.seenMessageIDs[message.MessageID]
		if message.MessageID != "" && exists {
			continue
		}
		if conversation.conversationRepository != nil {
			err = conversation.conversationRepository.AppendMessage(ctx, message)
			if err != nil {
				return err
			}
		}
		if message.MessageID != "" {
			conversation.seenMessageIDs[message.MessageID] = struct{}{}
		}

		conversation.messages = append(conversation.messages, message)
		conversation.addMessageUsage(message)
		conversation.historySequence = max(conversation.historySequence, message.Seq)
	}
	return nil
}
func (conversation *Conversation) GetHistory(context.Context) []*messagepkg.Message {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return append([]*messagepkg.Message(nil), conversation.messages...)
}
func (conversation *Conversation) BuildRequest(ctx context.Context, prompts []*messagepkg.Message) ([]*messagepkg.Message, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	request := make([]*messagepkg.Message, 0, len(prompts)+len(conversation.messages))
	request = append(request, prompts...)
	return append(request, conversation.messages...), nil
}

// 消息身份只分配一次；重投、checkpoint 和数据库使用同一个 MessageID。
func (conversation *Conversation) initializeMessage(ctx context.Context, runID string, message *messagepkg.Message) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if message.MessageID == "" && conversation.messageID != nil {
		message.MessageID, err = conversation.messageID(ctx, message)
		if err != nil {
			return err
		}
	}
	message.ThreadID = conversation.threadID
	if message.RunID == "" {
		message.RunID = runID
	}
	if message.CreatedAt == 0 {
		message.CreatedAt = time.Now().Unix()
	}
	return nil
}
func (conversation *Conversation) ReloadHistory(ctx context.Context) error {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if conversation.conversationRepository == nil {
		return nil
	}
	messages, messageIDs, sequence, err := conversation.conversationRepository.LoadContext(ctx, conversation.threadID)
	if err != nil {
		return err
	}
	seenMessageIDs := make(map[string]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		seenMessageIDs[messageID] = struct{}{}
	}
	conversation.messages = messages
	conversation.seenMessageIDs = seenMessageIDs
	conversation.historySequence = sequence
	conversation.recomputeContextUsage()
	return nil
}
