package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
)

// Transcript opens an independently owned writer per run. The caller chooses
// storage and naming; the middleware has no filesystem or session dependency.
type Transcript struct {
	BaseMiddleware
	Open   func(context.Context, string, string) (io.WriteCloser, error)
	mu     sync.Mutex
	writer io.WriteCloser
	seen   [][32]byte
}

func (*Transcript) GetName() string { return "transcript" }

func (transcript *Transcript) NewRun() Middleware { return &Transcript{Open: transcript.Open} }

func (transcript *Transcript) PrepareRun(ctx context.Context, runState *types.RunState) error {
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	if transcript.writer != nil {
		return fmt.Errorf("transcript already open")
	}
	transcript.seen = nil
	if transcript.Open == nil {
		return nil
	}
	transcriptWriter, err := transcript.Open(ctx, runState.ThreadID, runState.RunID)
	if err != nil {
		if transcriptWriter != nil {
			_ = transcriptWriter.Close()
		}
		return err
	}
	if transcriptWriter == nil {
		return fmt.Errorf("transcript opener returned nil writer")
	}
	transcript.writer = transcriptWriter
	return nil
}

func (transcript *Transcript) FinishRun(context.Context, *types.RunState, error) error {
	return transcript.Close(context.Background())
}

func (transcript *Transcript) Close(context.Context) error {
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	if transcript.writer == nil {
		return nil
	}
	transcriptWriter := transcript.writer
	transcript.writer = nil
	return transcriptWriter.Close()
}

// The existing time/role/content/tools fields remain readable by transcript
// consumers. Extra identity and reasoning fields preserve useful source data.
type transcriptMessage struct {
	Time       time.Time           `json:"time"`
	Role       string              `json:"role"`
	Content    string              `json:"content,omitempty"`
	Tools      []string            `json:"tools,omitempty"`
	Sequence   uint64              `json:"sequence"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	Reasoning  string              `json:"reasoning,omitempty"`
	Message    *messagepkg.Message `json:"message"`
}

func (transcript *Transcript) Observe(_ context.Context, runtimeEvent types.RuntimeEvent) error {
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	if transcript.writer == nil {
		return nil
	}
	var messages []*messagepkg.Message
	var isSnapshot bool
	switch runtimeEvent.Kind {
	case "llm_requesting":
		eventPayload, _ := runtimeEvent.Data.(types.LLMRequestingPayload)
		messages = eventPayload.Messages
		isSnapshot = true
	case "llm_end":
		eventPayload, _ := runtimeEvent.Data.(types.LLMEnd)
		messages = []*messagepkg.Message{eventPayload.Message}
	case "tool_end":
		eventPayload, ok := runtimeEvent.Data.(types.ToolEndPayload)
		if ok {
			messages = []*messagepkg.Message{messagepkg.NewToolMessage(eventPayload.Result, eventPayload.CallID)}
			messages[0].ToolName = eventPayload.Name
			if len(eventPayload.MultiContent) > 0 {
				messages[0].Content = ""
				messages[0].UserInputMultiContent = eventPayload.MultiContent
			}
		}
	default:
		return nil
	}
	messageHashes := make([][32]byte, len(messages))
	for i, message := range messages {
		// 事件与历史中的同一内容可能有不同业务元数据；元数据不参与重复判断。
		encodedMessage, err := json.Marshal(messagepkg.ToEino(message))
		if err != nil {
			return err
		}
		messageHashes[i] = sha256.Sum256(encodedMessage)
	}
	firstNewMessage := 0
	if isSnapshot {
		for firstNewMessage < len(messageHashes) && firstNewMessage < len(transcript.seen) && messageHashes[firstNewMessage] == transcript.seen[firstNewMessage] {
			firstNewMessage++
		}
	}
	for _, message := range messages[firstNewMessage:] {
		if message == nil {
			continue
		}
		transcriptRecord := transcriptMessage{Time: time.Now().UTC(), Role: string(message.Role), Content: message.Content, Sequence: runtimeEvent.Sequence, ToolCallID: message.ToolCallID, Reasoning: message.ReasoningContent, Message: message}
		for _, toolCall := range message.ToolCalls {
			transcriptRecord.Tools = append(transcriptRecord.Tools, toolCall.Function.Name)
		}
		err := json.NewEncoder(transcript.writer).Encode(transcriptRecord)
		if err != nil {
			return err
		}
	}
	if isSnapshot {
		transcript.seen = messageHashes
	} else {
		transcript.seen = append(transcript.seen, messageHashes...)
	}
	return nil
}
