package agentthread

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	deepagents "eino-cli/deepagent/core"
	deeptools "eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/model"
	toolpkg "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type memoryCheckpoints struct {
	mu   sync.Mutex
	data map[string][]byte
}

var _ compose.CheckPointStore = (*memoryCheckpoints)(nil)

func (s *memoryCheckpoints) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	return append([]byte(nil), value...), ok, nil
}

func (s *memoryCheckpoints) Set(_ context.Context, id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[id] = append([]byte(nil), value...)
	return nil
}

type scriptedModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
	stream func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message]
}

type echoTool struct{}

func (echoTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "echo"}, nil
}

func (echoTool) InvokableRun(context.Context, string, ...toolpkg.Option) (string, error) {
	return "ok", nil
}

func (m *scriptedModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *scriptedModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("Generate must not be used")
}

func (m *scriptedModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	index := len(m.inputs)
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	m.mu.Unlock()
	return m.stream(ctx, index, input), nil
}

func messageStream(messages ...*schema.Message) *schema.StreamReader[*schema.Message] {
	return schema.StreamReaderFromArray(messages)
}

func waitRunEnd(t *testing.T, events <-chan Event) Event {
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
	chatModel := &scriptedModel{stream: func(ctx context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index > 0 {
			return messageStream(schema.AssistantMessage("second", nil))
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
		Model: chatModel, CheckpointStore: &memoryCheckpoints{},
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
	waitRunEnd(t, events)

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
	chatModel := &scriptedModel{stream: func(ctx context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index > 0 {
			return messageStream(schema.AssistantMessage("recovered", nil))
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
		Model: chatModel, CheckpointStore: &memoryCheckpoints{},
	}}, events, ThreadOptions{})
	if _, err := thread.SubmitInput(context.Background(), schema.UserMessage("original")); err != nil {
		t.Fatal(err)
	}
	<-started
	timeout := 200 * time.Millisecond
	if !thread.Interrupt(InterruptOptions{Timeout: &timeout}) {
		t.Fatal("active run did not accept interrupt")
	}

	deadline := time.After(5 * time.Second)
	for thread.ActiveRun() != nil {
		select {
		case <-events:
		case <-deadline:
			t.Fatal("interrupted run did not finish")
		}
	}
	if _, err := thread.SubmitInput(context.Background(), schema.UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	waitRunEnd(t, events)

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
	chatModel := &scriptedModel{stream: func(_ context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index == 0 {
			return messageStream(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				ID: "question-1", Type: "function", Function: schema.FunctionCall{
					Name: "ask_user", Arguments: `{"question":"Which format?","options":["JSON","YAML"]}`,
				},
			}}})
		}
		return messageStream(schema.AssistantMessage("using YAML", nil))
	}}
	checkpoints := &memoryCheckpoints{}
	config := &RunConfig{Agent: deepagents.Config{
		Model: chatModel, CheckpointStore: checkpoints,
		HITLConfig: &deepagents.HITLConfig{NeedFollowUpTool: true},
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
	waitRunEnd(t, secondEvents)

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
	store := &dedupHistoryStore{
		seen: map[int64]struct{}{1: {}, 2: {}},
		records: []*HistoryRecord{
			{Type: HistoryRecordMessage, ThreadID: "thread-1", MessageID: 1, Seq: 1, Message: schema.UserMessage("previous")},
			{Type: HistoryRecordMessage, ThreadID: "thread-1", MessageID: 2, Seq: 2, Message: &schema.Message{
				Role:      schema.Assistant,
				ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "write_file", Arguments: `{}`}}},
			}},
		},
	}
	chatModel := &scriptedModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return messageStream(schema.AssistantMessage("recovered", nil))
	}}
	events := make(chan Event, 64)
	thread := New("thread-1", &RunConfig{Agent: deepagents.Config{
		Model: chatModel, CheckpointStore: &memoryCheckpoints{},
	}}, events, ThreadOptions{HistoryStore: store})
	if err := thread.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := thread.SubmitInput(context.Background(), schema.UserMessage("new task")); err != nil {
		t.Fatal(err)
	}
	waitRunEnd(t, events)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(chatModel.inputs))
	}
	messages := chatModel.inputs[0]
	if len(messages) != 4 || messages[2].Role != schema.Tool || messages[2].ToolCallID != "call-1" || messages[3].Content != "new task" {
		t.Fatalf("interrupted tool history was not repaired: %+v", messages)
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
			chatModel := &scriptedModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
				return messageStream(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
					ID: "call", Type: "function", Function: schema.FunctionCall{Name: "echo", Arguments: `{}`},
				}}})
			}}
			events := make(chan Event, 64)
			thread := New("thread-1", &RunConfig{Agent: deepagents.Config{
				Model: chatModel, Tools: []toolpkg.BaseTool{echoTool{}},
				MaxSteps: test.maxSteps, MaxModelCalls: test.maxModelCalls,
				CheckpointStore: &memoryCheckpoints{},
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
