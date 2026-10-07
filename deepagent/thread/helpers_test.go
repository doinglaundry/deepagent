package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/types"
	runpkg "eino-cli/deepagent/run"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type unreadableCheckpoint struct {
	compose.CheckPointStore
	err error
}

func (s unreadableCheckpoint) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, s.err
}

type usageThreadModel struct{ threadModel }

func (m *usageThreadModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *usageThreadModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input)
	message := schema.AssistantMessage("done", nil)
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type legacyParityMemoryCheckpoints struct {
	mu   sync.Mutex
	data map[string][]byte
}

var _ compose.CheckPointStore = (*legacyParityMemoryCheckpoints)(nil)

func (s *legacyParityMemoryCheckpoints) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	return append([]byte(nil), value...), ok, nil
}

func (s *legacyParityMemoryCheckpoints) Set(_ context.Context, id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[id] = append([]byte(nil), value...)
	return nil
}

type legacyParityScriptedModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
	stream func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message]
}

type legacyParityEchoTool struct{}

func (legacyParityEchoTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "echo"}, nil
}

func (legacyParityEchoTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "ok", nil
}

func (m *legacyParityScriptedModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *legacyParityScriptedModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("Generate must not be used")
}

func (m *legacyParityScriptedModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	index := len(m.inputs)
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	m.mu.Unlock()
	return m.stream(ctx, index, input), nil
}

func legacyParityMessageStream(messages ...*schema.Message) *schema.StreamReader[*schema.Message] {
	return schema.StreamReaderFromArray(messages)
}

func legacyParityWaitRunEnd(t *testing.T, events <-chan runpkg.Event) runpkg.Event {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Type == runpkg.EventError {
				t.Fatalf("run failed: %+v", event.Payload)
			}
			if event.Type == runpkg.EventRunEnd {
				return event
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for run end")
		}
	}
}

type legacyParityDedupConversationRepository struct {
	mu      sync.Mutex
	records []*dalmodel.ConversationEntry
	seen    map[int64]struct{}
}

func (s *legacyParityDedupConversationRepository) Append(_ context.Context, record *dalmodel.ConversationEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[int64]struct{})
	}
	_, exists := s.seen[record.MessageID]
	if exists {
		return nil
	}
	s.seen[record.MessageID] = struct{}{}
	copy := *record
	record.Seq = int64(len(s.records) + 1)
	copy.Seq = record.Seq
	s.records = append(s.records, &copy)
	return nil
}

func (s *legacyParityDedupConversationRepository) LoadAfter(_ context.Context, threadID string, sequence int64, limit int) ([]*dalmodel.ConversationEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []*dalmodel.ConversationEntry
	for _, record := range s.records {
		if record.ThreadID != threadID || record.Seq <= sequence {
			continue
		}
		records = append(records, record)
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	return records, nil
}

type redeliveryModel struct{}

func (m *redeliveryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *redeliveryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("done", nil), nil
}

func (m *redeliveryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
}

type redeliveryConversationRepository struct{ records []*dalmodel.ConversationEntry }

func (s *redeliveryConversationRepository) Append(_ context.Context, record *dalmodel.ConversationEntry) error {
	record.Seq = int64(len(s.records) + 1)
	s.records = append(s.records, record)
	return nil
}

func (s *redeliveryConversationRepository) LoadAfter(_ context.Context, threadID string, sequence int64, limit int) ([]*dalmodel.ConversationEntry, error) {
	var result []*dalmodel.ConversationEntry
	for _, record := range s.records {
		if record.ThreadID != threadID || record.Seq <= sequence {
			continue
		}
		result = append(result, record)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

type pendingQuestionModel struct {
	resumeModel
	started, release chan struct{}
}

func (m *pendingQuestionModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *pendingQuestionModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.calls == 0 {
		close(m.started)
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.resumeModel.Stream(ctx, input, opts...)
}

type pendingCheckpointStore struct {
	threadCheckpointMemory
	holdOnCompleted  bool
	started, release chan struct{}
	failure          error
	gateOnce         sync.Once
	gateErr          error
}

func (s *pendingCheckpointStore) Set(ctx context.Context, id string, raw []byte) error {
	var envelope checkpointer.Envelope
	err := json.Unmarshal(raw, &envelope)
	if err != nil {
		return err
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	decodeErr := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
	if decodeErr != nil {
		return decodeErr
	}
	state := snapshot.MapValues["State"].JSONValue
	hold := len(state.Consumed) > 1 && state.PreparedInputs < len(state.Consumed)
	if s.holdOnCompleted {
		hold = state.Phase == types.PhaseCompleted
	}
	if hold {
		s.gateOnce.Do(func() {
			close(s.started)
			select {
			case <-s.release:
			case <-ctx.Done():
				s.gateErr = ctx.Err()
			}
		})
		if s.gateErr != nil {
			return s.gateErr
		}
		if s.failure != nil {
			return s.failure
		}
	}
	return s.threadCheckpointMemory.Set(ctx, id, raw)
}

type pendingConversationRepository struct{ historyMemory }

func (s *pendingConversationRepository) Append(ctx context.Context, record *dalmodel.ConversationEntry) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	return s.historyMemory.Append(ctx, record)
}

type pendingSaveStore struct {
	historyMemory
	started chan struct{}
	release chan struct{}
	failure error
}

func (s *pendingSaveStore) Append(ctx context.Context, r *dalmodel.ConversationEntry) error {
	if r.Message.Content == "accepted pending" {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if s.failure != nil {
			return s.failure
		}
	}
	return s.historyMemory.Append(ctx, r)
}

type publicModel struct {
	infos  []*schema.ToolInfo
	inputs [][]*schema.Message
	call   bool
}

func (m *publicModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.infos = infos
	return m, nil
}

func (m *publicModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (m *publicModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input)
	message := schema.AssistantMessage("done", nil)
	if m.call && len(m.inputs) == 1 {
		message = schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: `{"delta":3}`}}})
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type threadCloseModel struct{ started, release chan struct{} }

func (m *threadCloseModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (*threadCloseModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("unexpected Generate")
}

func (m *threadCloseModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	close(m.started)
	<-m.release
	return nil, ctx.Err()
}

type threadCloseMiddleware struct {
	middleware.BaseMiddleware
	ready  chan struct{}
	closed atomic.Int32
}

func (*threadCloseMiddleware) GetName() string { return "thread_close" }

func (m *threadCloseMiddleware) GetStateHandler() types.RunTimeStateful {
	close(m.ready)
	return nil
}

func (m *threadCloseMiddleware) Close(context.Context) error { m.closed.Add(1); return nil }

type threadModel struct {
	mu      sync.Mutex
	calls   int
	inputs  [][]*schema.Message
	started chan struct{}
	release chan struct{}
}

func (m *threadModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *threadModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}

func (m *threadModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	index := m.calls
	m.calls++
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	m.mu.Unlock()
	if index == 0 && m.started != nil {
		close(m.started)
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("answer", nil)}), nil
}

type historyMemory struct{ records []*dalmodel.ConversationEntry }

func (s *historyMemory) Append(_ context.Context, r *dalmodel.ConversationEntry) error {
	r.Seq = int64(len(s.records) + 1)
	if r.MessageID == 0 {
		r.MessageID = r.Seq
	}
	s.records = append(s.records, r)
	return nil
}

func (s *historyMemory) LoadAfter(_ context.Context, threadID string, sequence int64, limit int) ([]*dalmodel.ConversationEntry, error) {
	var result []*dalmodel.ConversationEntry
	for _, r := range s.records {
		if r.ThreadID != threadID || r.Seq <= sequence {
			continue
		}
		result = append(result, r)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

type threadCheckpointMemory struct{ values map[string][]byte }

func (s *threadCheckpointMemory) Get(_ context.Context, id string) ([]byte, bool, error) {
	v, ok := s.values[id]
	return v, ok, nil
}

func (s *threadCheckpointMemory) Set(_ context.Context, id string, v []byte) error {
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[id] = append([]byte(nil), v...)
	return nil
}

type resumeModel struct {
	calls  int
	inputs [][]*schema.Message
}

func (m *resumeModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *resumeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}

func (m *resumeModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if m.calls == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "question", Type: "function", Function: schema.FunctionCall{Name: "ask_user", Arguments: `{"question":"which one?","options":["a","b"]}`}}})}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
}

type resumedInputModel struct {
	resumeModel
	started chan struct{}
	release chan struct{}
}

func (m *resumedInputModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *resumedInputModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.resumeModel.Stream(ctx, input, opts...)
	if m.calls == 2 {
		close(m.started)
		select {
		case <-m.release:
		case <-ctx.Done():
			stream.Close()
			return nil, ctx.Err()
		}
	}
	return stream, err
}

func newTestThread(threadID string, cfg *runpkg.Config, events chan runpkg.Event, options ThreadOptions) *Thread {
	thread, err := NewThread(ThreadConfig{ThreadID: threadID, RunConfig: cfg, Events: events, Options: options})
	if err != nil {
		panic(err)
	}
	return thread
}
