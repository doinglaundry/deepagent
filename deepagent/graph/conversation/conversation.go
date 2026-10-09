package conversation

import (
	"context"
	"slices"
	"sync"
	"time"

	agentmodel "eino-cli/deepagent/model"
	"eino-cli/deepagent/utils"
)

var _ agentmodel.Conversation = (*Conversation)(nil)

// Conversation publishes changes only after durable writes succeed.
type Conversation struct {
	mu                sync.Mutex
	threadID          string
	messages          []*agentmodel.Message
	seenMessageIDs    map[string]struct{}
	historySequence   int64
	conversationDB    agentmodel.ConversationDB
	compactor         *SummaryCompaction
	generateMessageID agentmodel.GetMessageIDFunc
	countTokenFunc    agentmodel.CountTokenFunc
	contextTokenUsage agentmodel.ContextTokenUsage
	runUsage          agentmodel.RunUsage
}

func New(
	threadID string,
	conversationDB agentmodel.ConversationDB,
	compactor *SummaryCompaction,
	countTokenFunc agentmodel.CountTokenFunc,
	maxContextTokens int64,
	generateMessageID agentmodel.GetMessageIDFunc,
) *Conversation {
	if countTokenFunc == nil {
		countTokenFunc = utils.SimpleTokenCounter
	}
	conversation := &Conversation{
		threadID:          threadID,
		conversationDB:    conversationDB,
		compactor:         compactor,
		countTokenFunc:    countTokenFunc,
		generateMessageID: generateMessageID,
		seenMessageIDs:    make(map[string]struct{}),
		contextTokenUsage: agentmodel.ContextTokenUsage{MaxContextTokens: maxContextTokens},
	}
	conversation.recomputeContextUsage()
	return conversation
}
func (conversation *Conversation) AddHistory(ctx context.Context, runID string, messages ...*agentmodel.Message) error {
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
		if conversation.conversationDB != nil {
			err = conversation.conversationDB.AppendMessage(ctx, message)
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
func (conversation *Conversation) GetHistory(context.Context) []*agentmodel.Message {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	return slices.Clone(conversation.messages)
}
func (conversation *Conversation) BuildRequest(ctx context.Context, prompts []*agentmodel.Message) ([]*agentmodel.Message, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	request := make([]*agentmodel.Message, 0, len(prompts)+len(conversation.messages))
	request = append(request, prompts...)
	return append(request, conversation.messages...), nil
}

// 消息身份只分配一次；重投、checkpoint 和数据库使用同一个 MessageID。
func (conversation *Conversation) initializeMessage(ctx context.Context, runID string, message *agentmodel.Message) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if message.MessageID == "" && conversation.generateMessageID != nil {
		message.MessageID, err = conversation.generateMessageID(ctx, message)
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
	if conversation.conversationDB == nil {
		return nil
	}
	messages, recordedMessageIDs, lastReadSeq, err := conversation.conversationDB.LoadContext(ctx, conversation.threadID)
	if err != nil {
		return err
	}
	seenMessageIDs := make(map[string]struct{}, len(recordedMessageIDs))
	for _, messageID := range recordedMessageIDs {
		seenMessageIDs[messageID] = struct{}{}
	}
	conversation.messages = messages
	conversation.seenMessageIDs = seenMessageIDs
	conversation.historySequence = lastReadSeq
	conversation.recomputeContextUsage()
	return nil
}
