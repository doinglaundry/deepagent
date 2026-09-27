package agentthread

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	deepagents "eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/internal/conversation"
	deeptools "eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/model"
	toolpkg "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

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

func (legacyParityEchoTool) InvokableRun(context.Context, string, ...toolpkg.Option) (string, error) {
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

func legacyParityWaitRunEnd(t *testing.T, events <-chan Event) Event {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Type == EventError {
				t.Fatalf("run failed: %+v", event.Payload)
			}
			if event.Type == EventRunEnd {
				return event
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for run end")
		}
	}
}

func TestPendingInputStaysInOneRun(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	chatModel := &legacyParityScriptedModel{stream: func(ctx context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index > 0 {
			return legacyParityMessageStream(schema.AssistantMessage("second", nil))
		}
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() {
			defer writer.Close()
			close(started)
			select {
			case <-gate:
				writer.Send(schema.AssistantMessage("first", nil), nil)
			case <-ctx.Done():
			}
		}()
		return reader
	}}

	events := make(chan Event, 64)
	thread := New("thread-1", &RunConfig{Agent: deepagents.Config{
		Model: chatModel, CheckpointStore: &legacyParityMemoryCheckpoints{},
	}}, events, ThreadOptions{})
	first, err := thread.SubmitInput(context.Background(), schema.UserMessage("first"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := thread.SubmitInput(context.Background(), schema.UserMessage("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != second.RunID || second.Started {
		t.Fatalf("pending input started another run: first=%+v second=%+v", first, second)
	}
	close(gate)
	legacyParityWaitRunEnd(t, events)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(chatModel.inputs))
	}
	last := chatModel.inputs[1]
	if len(last) == 0 || last[len(last)-1].Content != "second" {
		t.Fatalf("pending input was not delivered to the same run: %+v", last)
	}
}

func TestInterruptedRunLeavesThreadReusable(t *testing.T) {
	started := make(chan struct{})
	chatModel := &legacyParityScriptedModel{stream: func(ctx context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index > 0 {
			return legacyParityMessageStream(schema.AssistantMessage("recovered", nil))
		}
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() {
			defer writer.Close()
			close(started)
			<-ctx.Done()
		}()
		return reader
	}}

	events := make(chan Event, 64)
	thread := New("thread-1", &RunConfig{Agent: deepagents.Config{
		Model: chatModel, CheckpointStore: &legacyParityMemoryCheckpoints{},
	}}, events, ThreadOptions{})
	if _, err := thread.SubmitInput(context.Background(), schema.UserMessage("original")); err != nil {
		t.Fatal(err)
	}
	<-started
	handle := thread.ActiveRun()
	timeout := 200 * time.Millisecond
	if !thread.Interrupt(InterruptOptions{Timeout: &timeout}) {
		t.Fatal("active run did not accept interrupt")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = handle.Wait(waitCtx) // An interrupted run may return its interruption error.
	if waitCtx.Err() != nil || thread.ActiveRun() != nil {
		t.Fatal("interrupted run did not finish")
	}
	// Terminal events precede completion. Drain the interrupted run before
	// asserting the next run succeeds; no extra event signals becoming inactive.
	for len(events) > 0 {
		<-events
	}
	if _, err := thread.SubmitInput(context.Background(), schema.UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	legacyParityWaitRunEnd(t, events)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(chatModel.inputs))
	}
	second := chatModel.inputs[1]
	if len(second) < 2 || second[0].Content != "original" || second[len(second)-1].Content != "again" {
		t.Fatalf("history was not reusable after interrupt: %+v", second)
	}
}

func TestBlockedRunResumesFromCheckpointOnNewThread(t *testing.T) {
	chatModel := &legacyParityScriptedModel{stream: func(_ context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index == 0 {
			return legacyParityMessageStream(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				ID: "question-1", Type: "function", Function: schema.FunctionCall{
					Name: "ask_user", Arguments: `{"question":"Which format?","options":["JSON","YAML"]}`,
				},
			}}})
		}
		return legacyParityMessageStream(schema.AssistantMessage("using YAML", nil))
	}}
	checkpoints := &legacyParityMemoryCheckpoints{}
	config := &RunConfig{Agent: deepagents.Config{
		Model: chatModel, CheckpointStore: checkpoints,
		ToolDescriptors: []deeptools.Descriptor{deeptools.Describe(deeptools.GetFollowUpTool())},
	}}

	firstEvents := make(chan Event, 64)
	first := New("thread-1", config, firstEvents, ThreadOptions{})
	started, err := first.SubmitInput(context.Background(), schema.UserMessage("export"))
	if err != nil {
		t.Fatal(err)
	}
	var blocked FollowUpRequestedPayload
	deadline := time.After(5 * time.Second)
waitBlocked:
	for {
		select {
		case event := <-firstEvents:
			switch event.Type {
			case EventFollowUpRequested:
				blocked = event.Payload.(FollowUpRequestedPayload)
				break waitBlocked
			case EventError:
				t.Fatalf("first run failed: %+v", event.Payload)
			}
		case <-deadline:
			t.Fatal("timed out waiting for follow-up request")
		}
	}
	for first.ActiveRun() != nil {
		time.Sleep(time.Millisecond)
	}
	if blocked.CheckpointID == "" || blocked.InterruptID == "" {
		t.Fatalf("incomplete block: %+v", blocked)
	}

	secondEvents := make(chan Event, 64)
	second := New("thread-1", config, secondEvents, ThreadOptions{})
	if _, err = second.ResumeRun(context.Background(), started.RunID, ResumeRunOptions{
		CheckpointID: blocked.CheckpointID,
		ResumeData: map[string]any{
			blocked.InterruptID: &deeptools.FollowUpInfo{UserAnswer: "YAML"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	legacyParityWaitRunEnd(t, secondEvents)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(chatModel.inputs))
	}
	messages := chatModel.inputs[1]
	if len(messages) == 0 || messages[len(messages)-1].Role != schema.Tool || messages[len(messages)-1].Content != "YAML" {
		t.Fatalf("resume answer did not reach checkpointed tool call: %+v", messages)
	}
}

func TestReloadRepairsInterruptedToolCallBeforeNewInput(t *testing.T) {
	store := &legacyParityDedupHistoryStore{
		seen: map[int64]struct{}{1: {}, 2: {}},
		records: []*HistoryRecord{
			{Type: conversation.HistoryRecordMessage, ThreadID: "thread-1", MessageID: 1, Seq: 1, Message: schema.UserMessage("previous")},
			{Type: conversation.HistoryRecordMessage, ThreadID: "thread-1", MessageID: 2, Seq: 2, Message: &schema.Message{
				Role:      schema.Assistant,
				ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "write_file", Arguments: `{}`}}},
			}},
		},
	}
	chatModel := &legacyParityScriptedModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return legacyParityMessageStream(schema.AssistantMessage("recovered", nil))
	}}
	events := make(chan Event, 64)
	thread := New("thread-1", &RunConfig{Agent: deepagents.Config{
		Model: chatModel, CheckpointStore: &legacyParityMemoryCheckpoints{},
	}}, events, ThreadOptions{HistoryStore: store})
	if err := thread.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := thread.SubmitInput(context.Background(), schema.UserMessage("new task")); err != nil {
		t.Fatal(err)
	}
	legacyParityWaitRunEnd(t, events)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(chatModel.inputs))
	}
	messages := chatModel.inputs[0]
	if len(messages) != 4 || messages[2].Role != schema.Tool || messages[2].ToolCallID != "call-1" || messages[3].Content != "new task" {
		t.Fatalf("interrupted tool history was not repaired: %+v", messages)
	}
	for _, message := range thread.ContextManager().History(context.Background()) {
		if message.Role == schema.Tool && message.ToolCallID == "call-1" {
			t.Fatal("synthetic repair was persisted as a real tool result")
		}
	}
}

func TestRunBudgetsStopUnboundedToolLoop(t *testing.T) {
	for _, test := range []struct {
		name          string
		maxSteps      int
		maxModelCalls int
	}{
		{name: "model calls", maxSteps: 20, maxModelCalls: 1},
		{name: "graph steps", maxSteps: 1, maxModelCalls: 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			chatModel := &legacyParityScriptedModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
				return legacyParityMessageStream(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
					ID: "call", Type: "function", Function: schema.FunctionCall{Name: "echo", Arguments: `{}`},
				}}})
			}}
			events := make(chan Event, 64)
			thread := New("thread-1", &RunConfig{Agent: deepagents.Config{
				Model: chatModel, ToolDescriptors: []deeptools.Descriptor{{Tool: legacyParityEchoTool{}}},
				MaxSteps: test.maxSteps, MaxModelCalls: test.maxModelCalls,
				CheckpointStore: &legacyParityMemoryCheckpoints{},
			}}, events, ThreadOptions{})
			if _, err := thread.SubmitInput(context.Background(), schema.UserMessage("loop")); err != nil {
				t.Fatal(err)
			}
			deadline := time.After(5 * time.Second)
			for {
				select {
				case event := <-events:
					if event.Type == EventError {
						return
					}
				case <-deadline:
					t.Fatal("budget did not stop tool loop")
				}
			}
		})
	}
}

type legacyParityDedupHistoryStore struct {
	mu      sync.Mutex
	records []*HistoryRecord
	seen    map[int64]struct{}
}

func (s *legacyParityDedupHistoryStore) Append(_ context.Context, record *HistoryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[int64]struct{})
	}
	if _, exists := s.seen[record.MessageID]; exists {
		return nil
	}
	s.seen[record.MessageID] = struct{}{}
	copy := *record
	copy.Seq = int64(len(s.records) + 1)
	s.records = append(s.records, &copy)
	return nil
}

func (s *legacyParityDedupHistoryStore) List(_ context.Context, query ListQuery) ([]*HistoryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := append([]*HistoryRecord(nil), s.records...)
	if query.Order == ListOrderDESC {
		for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
			result[left], result[right] = result[right], result[left]
		}
	}
	return result, nil
}
