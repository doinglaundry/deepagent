package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
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

func (*Transcript) Name() string         { return "transcript" }
func (t *Transcript) NewRun() Middleware { return &Transcript{Open: t.Open} }
func (t *Transcript) BeforeRun(ctx context.Context, state *types.RunState) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.writer != nil {
		return fmt.Errorf("transcript already open")
	}
	t.seen = nil
	if t.Open == nil {
		return nil
	}
	writer, err := t.Open(ctx, state.ThreadID, state.RunID)
	if err != nil {
		if writer != nil {
			_ = writer.Close()
		}
		return err
	}
	if writer == nil {
		return fmt.Errorf("transcript opener returned nil writer")
	}
	t.writer = writer
	return nil
}
func (t *Transcript) AfterRun(context.Context, *types.RunState, error) error {
	return t.Close(context.Background())
}
func (t *Transcript) Close(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.writer == nil {
		return nil
	}
	writer := t.writer
	t.writer = nil
	return writer.Close()
}

// The existing time/role/content/tools fields remain readable by transcript
// consumers. Extra identity and reasoning fields preserve useful source data.
type transcriptMessage struct {
	Time       time.Time       `json:"time"`
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	Tools      []string        `json:"tools,omitempty"`
	Sequence   uint64          `json:"sequence"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Reasoning  string          `json:"reasoning,omitempty"`
	Message    *schema.Message `json:"message"`
}

func (t *Transcript) Observe(_ context.Context, event types.RuntimeEvent) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.writer == nil {
		return nil
	}
	var messages []*schema.Message
	var snapshot bool
	switch event.Kind {
	case "llm_requesting":
		payload, _ := event.Data.(types.LLMRequestingPayload)
		messages = payload.Messages
		snapshot = true
	case "llm_end":
		payload, _ := event.Data.(types.LLMEnd)
		messages = []*schema.Message{payload.Message}
	case "tool_end":
		payload, ok := event.Data.(types.ToolEndPayload)
		if ok {
			messages = []*schema.Message{schema.ToolMessage(payload.Result, payload.CallID)}
			if len(payload.MultiContent) > 0 {
				messages[0].Content = ""
				messages[0].UserInputMultiContent = payload.MultiContent
			}
		}
	default:
		return nil
	}
	hashes := make([][32]byte, len(messages))
	for i, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}
		hashes[i] = sha256.Sum256(raw)
	}
	start := 0
	if snapshot {
		for start < len(hashes) && start < len(t.seen) && hashes[start] == t.seen[start] {
			start++
		}
	}
	for _, message := range messages[start:] {
		if message == nil {
			continue
		}
		record := transcriptMessage{Time: time.Now().UTC(), Role: string(message.Role), Content: message.Content, Sequence: event.Sequence, ToolCallID: message.ToolCallID, Reasoning: message.ReasoningContent, Message: message}
		for _, call := range message.ToolCalls {
			record.Tools = append(record.Tools, call.Function.Name)
		}
		if err := json.NewEncoder(t.writer).Encode(record); err != nil {
			return err
		}
	}
	if snapshot {
		t.seen = hashes
	} else {
		t.seen = append(t.seen, hashes...)
	}
	return nil
}
