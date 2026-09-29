package agentthread

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

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
func TestThread_SubmitAndAppendUseSameRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	events := make(chan Event, 100)
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: model}}, events, ThreadOptions{})
	if err := thread.Init(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithInputMeta("id-1"))
	if err != nil {
		t.Fatal(err)
	}
	<-model.started
	second, err := thread.SubmitInput(ctx, schema.UserMessage("second"), WithInputMeta("id-2"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Started || second.RunID != first.RunID {
		t.Fatal("append assigned to different run")
	}
	close(model.release)
	if err := first.RunHandle.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("calls=%d", model.calls)
	}
	inputs := first.RunHandle.ConsumedInputs()
	meta := first.RunHandle.ConsumedInputsMeta()
	if len(inputs) != 2 || inputs[1].Content != "second" || len(meta) != 2 || meta[1] != "id-2" {
		t.Fatalf("lost input identity: %v %v", inputs, meta)
	}
}
func TestThread_SubmitInputRejectsInvalidMessageWhileIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	history := &historyMemory{}
	model := &threadModel{}
	events := make(chan Event, 100)
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: model}}, events, ThreadOptions{HistoryStore: history})

	unsupported := schema.UserMessage("unsupported")
	unsupported.Extra = map[string]any{"value": func() {}}
	cyclic := schema.UserMessage("cyclic")
	cyclicExtra := map[string]any{}
	cyclicExtra["self"] = cyclicExtra
	cyclic.Extra = cyclicExtra
	inputs := []struct {
		name    string
		message *schema.Message
	}{
		{name: "unsupported extra", message: unsupported},
		{name: "cyclic extra", message: cyclic},
	}

	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			result, err := thread.SubmitInput(ctx, input.message)
			if err == nil {
				t.Fatal("accepted message that could not be copied")
			}
			if result != nil {
				t.Fatal("returned a run for a rejected message")
			}
			if thread.ActiveRun() != nil {
				t.Fatal("started a run for a rejected message")
			}
			if model.calls != 0 {
				t.Fatalf("model calls=%d", model.calls)
			}
			if len(history.records) != 0 {
				t.Fatalf("history records=%d", len(history.records))
			}
			if len(events) != 0 {
				t.Fatalf("events=%d", len(events))
			}
		})
	}
}
func TestThread_SubmitInputRejectsInvalidMessageWhileActive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: model}}, make(chan Event, 100), ThreadOptions{})
	first, err := thread.SubmitInput(ctx, schema.UserMessage("first"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.started:
	case <-ctx.Done():
		t.Fatalf("model did not start: %v", ctx.Err())
	}

	unsupported := schema.UserMessage("unsupported")
	unsupported.Extra = map[string]any{"value": func() {}}
	cyclic := schema.UserMessage("cyclic")
	cyclicExtra := map[string]any{}
	cyclicExtra["self"] = cyclicExtra
	cyclic.Extra = cyclicExtra
	inputs := []struct {
		name    string
		message *schema.Message
	}{
		{name: "unsupported extra", message: unsupported},
		{name: "cyclic extra", message: cyclic},
	}
	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			result, err := thread.SubmitInput(ctx, input.message)
			if err == nil {
				t.Fatal("accepted message that could not be copied")
			}
			if result != nil {
				t.Fatal("returned a run for a rejected message")
			}
			thread.mu.Lock()
			pending := len(thread.pending)
			thread.mu.Unlock()
			if pending != 0 {
				t.Fatalf("pending inputs=%d", pending)
			}
		})
	}

	close(model.release)
	err = first.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls=%d", model.calls)
	}
	consumedInputs := first.RunHandle.ConsumedInputs()
	if len(consumedInputs) != 1 || consumedInputs[0].Content != "first" {
		t.Fatalf("consumed inputs=%v", consumedInputs)
	}
}
func TestRun_NoEventsAfterActiveRunBecomesNil(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events := make(chan Event) // Force producer to wait for each event publication.
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: &threadModel{}}}, events, ThreadOptions{})
	result, err := thread.SubmitInput(ctx, schema.UserMessage("input"))
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-events:
			if event.Type == EventRunEnd {
				if err := result.RunHandle.Wait(ctx); err != nil {
					t.Fatal(err)
				}
				if thread.ActiveRun() != nil {
					t.Fatal("still active after Wait")
				}
				select {
				case e := <-events:
					t.Fatalf("late event: %v", e.Type)
				default:
				}
				return
			}
			if thread.ActiveRun() == nil {
				t.Fatalf("inactive before event %s", event.Type)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func TestThread_InputAcceptedAtFinishBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: &threadModel{}}}, make(chan Event, 10000), ThreadOptions{})
	for i := 0; i < 50; i++ {
		first, err := thread.SubmitInput(ctx, schema.UserMessage("first"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := thread.SubmitInput(ctx, schema.UserMessage("boundary"))
		if err != nil {
			t.Fatal(err)
		}
		if err := first.RunHandle.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if err := second.RunHandle.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, input := range second.RunHandle.ConsumedInputs() {
			if input.Content == "boundary" {
				found = true
			}
		}
		if !found {
			t.Fatal("accepted input orphaned at finish boundary")
		}
	}
}

type historyMemory struct{ records []*HistoryRecord }

func (s *historyMemory) Append(_ context.Context, r *HistoryRecord) error {
	r.Seq = int64(len(s.records) + 1)
	if r.MessageID == 0 {
		r.MessageID = r.Seq
	}
	s.records = append(s.records, r)
	return nil
}
func (s *historyMemory) List(_ context.Context, q ListQuery) ([]*HistoryRecord, error) {
	var result []*HistoryRecord
	for _, r := range s.records {
		if q.AfterID != nil && r.Seq <= *q.AfterID {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

type checkpointMemory struct{ values map[string][]byte }

func (s *checkpointMemory) Get(_ context.Context, id string) ([]byte, bool, error) {
	v, ok := s.values[id]
	return v, ok, nil
}
func (s *checkpointMemory) Set(_ context.Context, id string, v []byte) error {
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
func TestRun_InterruptAndResumeOnNewThread(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	history := &historyMemory{}
	checkpoints := &checkpointMemory{}
	m := &resumeModel{}
	config := &RunConfig{Agent: graph.Config{Model: m, CheckpointStore: checkpoints, ToolDescriptors: []tools.ToolDescriptor{{Tool: tools.GetFollowUpTool()}}}}
	events := make(chan Event, 100)
	first := New("thread", config, events, ThreadOptions{HistoryStore: history})
	if err := first.Init(ctx); err != nil {
		t.Fatal(err)
	}
	started, err := first.SubmitInput(ctx, schema.UserMessage("ask me"), WithInputMeta(map[string]string{"MessageID": "9007199254740993", "Sender": "user"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := started.RunHandle.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	var question FollowUpRequestedPayload
	for len(events) > 0 {
		event := <-events
		if event.Type == EventFollowUpRequested {
			question = event.Payload.(FollowUpRequestedPayload)
		}
	}
	if question.InterruptID == "" || question.Info.Question != "which one?" {
		t.Fatalf("missing follow-up: %+v", question)
	}
	restored := New("thread", config, events, ThreadOptions{HistoryStore: history})
	if err := restored.Init(ctx); err != nil {
		t.Fatal(err)
	}
	hookCalled := false
	bad, badErr := restored.ResumeRun(ctx, started.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{"wrong-interrupt": &tools.FollowUpInfo{UserAnswer: "a"}}, OnRunStart: func(ctx context.Context, _ RunStartRequest) context.Context { hookCalled = true; return ctx }})
	if badErr == nil {
		_ = bad.Wait(ctx)
		t.Fatal("uncorrelated resume accepted")
	}
	if bad != nil || hookCalled || restored.ActiveRun() != nil || m.calls != 1 {
		t.Fatal("rejected resume performed work")
	}
	handle, err := restored.ResumeRun(ctx, started.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{question.InterruptID: &tools.FollowUpInfo{UserAnswer: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	meta := handle.ConsumedInputsMeta()
	if len(meta) != 1 {
		t.Fatalf("lost metadata: %v", meta)
	}
	identity, ok := meta[0].(map[string]string)
	if !ok || identity["MessageID"] != "9007199254740993" || identity["Sender"] != "user" {
		t.Fatalf("changed typed identity: %#v", meta)
	}
	if m.calls != 2 || len(m.inputs[1]) != 3 || m.inputs[1][2].Content != "a" {
		t.Fatalf("resume restarted model or lost tool answer: calls=%d inputs=%v", m.calls, m.inputs)
	}
	replay := New("thread", config, events, ThreadOptions{HistoryStore: history})
	if err := replay.Init(ctx); err != nil {
		t.Fatal(err)
	}
	handle, err = replay.ResumeRun(ctx, started.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{question.InterruptID: &tools.FollowUpInfo{UserAnswer: "a"}}})
	if err == nil {
		_ = handle.Wait(ctx)
		t.Fatal("completed checkpoint accepted as a new active run")
	}
	if handle != nil || replay.ActiveRun() != nil || m.calls != 2 {
		t.Fatal("rejected resume performed work")
	}
}
