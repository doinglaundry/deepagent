package agentthread

import (
	"context"
	backends "eino-cli/deepagent/core/tools/filesystem"
	"eino-cli/protocol"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
	stream func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message]
}

func (f *fakeModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return f, nil }
func (f *fakeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("Generate must not be used")
}
func (f *fakeModel) Stream(ctx context.Context, m []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	f.mu.Lock()
	i := len(f.inputs)
	f.inputs = append(f.inputs, append([]*schema.Message(nil), m...))
	f.mu.Unlock()
	return f.stream(ctx, i, m), nil
}
func chunks(ms ...*schema.Message) *schema.StreamReader[*schema.Message] {
	return schema.StreamReaderFromArray(ms)
}

type echoTool struct {
	count   int
	approve bool
}

func (e *echoTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "echo", Desc: "echo"}, nil
}
func (e *echoTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	e.count++
	return "tool result", nil
}
func (e *echoTool) RequiresApproval() bool { return e.approve }
func (e *echoTool) ReadOnly() bool         { return !e.approve }
func call() *schema.Message {
	return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call1", Type: "function", Function: schema.FunctionCall{Name: "echo", Arguments: "{}"}}}}
}
func input(s string) protocol.Input {
	return protocol.Input{ID: protocol.NewID("msg"), Kind: protocol.InputUser, Text: s}
}
func terminal(t *testing.T, th *Thread) []protocol.Event {
	t.Helper()
	var es []protocol.Event
	for {
		select {
		case e := <-th.Events():
			es = append(es, e)
			if e.Terminal() {
				return es
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no terminal event")
			return nil
		}
	}
}
func newThread(t *testing.T, c Config) *Thread {
	t.Helper()
	c.ThreadID = "thread"
	c.SessionID = "session"
	th, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { th.Close() })
	return th
}
func TestGraphToolIterationAndIncrementalOutput(t *testing.T) {
	gate := make(chan struct{})
	f := &fakeModel{stream: func(ctx context.Context, i int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if i == 0 {
			return chunks(call())
		}
		r, w := schema.Pipe[*schema.Message](0)
		go func() {
			defer w.Close()
			w.Send(&schema.Message{Role: schema.Assistant, Content: "one"}, nil)
			select {
			case <-gate:
			case <-ctx.Done():
				return
			}
			w.Send(&schema.Message{Role: schema.Assistant, Content: "two"}, nil)
		}()
		return r
	}}
	et := &echoTool{}
	th := newThread(t, Config{Model: f, Tools: []tool.BaseTool{et}})
	_, err := th.SubmitInput(context.Background(), input("hello"))
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case e := <-th.Events():
			if e.Kind == protocol.EventTextDelta {
				if e.Text != "one" {
					t.Fatal(e)
				}
				close(gate)
				goto finish
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no incremental output")
		}
	}
finish:
	es := terminal(t, th)
	if es[len(es)-1].Kind != protocol.EventRunCompleted {
		t.Fatal(es)
	}
	if et.count != 1 {
		t.Fatal(et.count)
	}
	if len(f.inputs) != 2 || f.inputs[1][len(f.inputs[1])-1].Role != schema.Tool {
		t.Fatal("tool result missing from model history")
	}
}
func TestPendingInputAtFinalBoundary(t *testing.T) {
	gate := make(chan struct{})
	f := &fakeModel{stream: func(ctx context.Context, i int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if i > 0 {
			return chunks(&schema.Message{Role: schema.Assistant, Content: "second"})
		}
		r, w := schema.Pipe[*schema.Message](1)
		w.Send(&schema.Message{Role: schema.Assistant, Content: "first"}, nil)
		go func() { <-gate; w.Close() }()
		return r
	}}
	th := newThread(t, Config{Model: f})
	id, _ := th.SubmitInput(context.Background(), input("first"))
	for e := range th.Events() {
		if e.Kind == protocol.EventTextDelta {
			break
		}
	}
	id2, e := th.SubmitInput(context.Background(), input("late"))
	if e != nil || id2 != id {
		t.Fatalf("%s %s %v", id, id2, e)
	}
	close(gate)
	terminal(t, th)
	if len(f.inputs) != 2 || f.inputs[1][len(f.inputs[1])-1].Content != "late" {
		t.Fatal("pending lost")
	}
}

type memoryStore struct {
	mu      sync.Mutex
	data    map[string][]byte
	history []*schema.Message
}

func (m *memoryStore) Get(_ context.Context, k string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[k]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return append([]byte(nil), v...), nil
}
func (m *memoryStore) Set(_ context.Context, k string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[k] = append([]byte(nil), v...)
	return nil
}
func (m *memoryStore) Load(context.Context) ([]*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*schema.Message(nil), m.history...), nil
}
func (m *memoryStore) Save(_ context.Context, h []*schema.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append([]*schema.Message(nil), h...)
	return nil
}
func TestApprovalResumesSameRunOnNewThread(t *testing.T) {
	store := &memoryStore{}
	et := &echoTool{approve: true}
	f := &fakeModel{stream: func(_ context.Context, i int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if i == 0 {
			return chunks(call())
		}
		return chunks(schema.AssistantMessage("done", nil))
	}}
	c := Config{Model: f, Tools: []tool.BaseTool{et}, History: store, Checkpoints: store}
	th := newThread(t, c)
	id, _ := th.SubmitInput(context.Background(), input("run"))
	es := terminal(t, th)
	end := es[len(es)-1]
	if end.Kind != protocol.EventBlocked || end.Block == nil || et.count != 0 {
		t.Fatalf("%+v calls=%d", end, et.count)
	}
	th.Close()
	th2 := newThread(t, c)
	resume := protocol.Input{ID: "resume", Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: id, CheckpointID: end.Block.CheckpointID, InterruptID: "wrong", Approved: true}}
	if _, e := th2.ResumeRun(context.Background(), resume); e == nil {
		t.Fatal("accepted wrong interrupt")
	}
	resume.Resume.InterruptID = end.Block.InterruptID
	id2, e := th2.ResumeRun(context.Background(), resume)
	if e != nil || id2 != id {
		t.Fatalf("resume %s %v", id2, e)
	}
	es = terminal(t, th2)
	if es[len(es)-1].Kind != protocol.EventRunCompleted || et.count != 1 {
		t.Fatalf("%+v calls %d", es, et.count)
	}
	th2.Close()
	th3 := newThread(t, c)
	if _, e := th3.ResumeRun(context.Background(), resume); e == nil {
		t.Fatal("completed checkpoint was replayed")
	}
}
func TestBudgetsCountModelAndGraphSeparately(t *testing.T) {
	for _, tc := range []struct {
		name         string
		steps, calls int
		want         string
	}{{"model", 20, 1, "MaxModelCalls"}, {"graph", 1, 20, "MaxSteps"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
				return chunks(call())
			}}
			th := newThread(t, Config{Model: f, Tools: []tool.BaseTool{&echoTool{}}, MaxSteps: tc.steps, MaxModelCalls: tc.calls})
			th.SubmitInput(context.Background(), input("go"))
			es := terminal(t, th)
			e := es[len(es)-1]
			if e.Kind != protocol.EventRunFailed || !strings.Contains(e.Error, tc.want) {
				t.Fatalf("%+v", e)
			}
		})
	}
}
func TestCancellationKeepsHistoryAndThreadUsable(t *testing.T) {
	entered := make(chan struct{})
	f := &fakeModel{stream: func(ctx context.Context, i int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if i > 0 {
			return chunks(schema.AssistantMessage("recovered", nil))
		}
		r, w := schema.Pipe[*schema.Message](0)
		go func() { close(entered); <-ctx.Done(); w.Close() }()
		return r
	}}
	store := &memoryStore{}
	th := newThread(t, Config{Model: f, History: store})
	th.SubmitInput(context.Background(), input("original"))
	<-entered
	th.Cancel()
	es := terminal(t, th)
	if es[len(es)-1].Kind != protocol.EventRunCancelled {
		t.Fatal(es)
	}
	for {
		_, e := th.SubmitInput(context.Background(), input("again"))
		if e == ErrDraining {
			time.Sleep(time.Millisecond)
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		break
	}
	terminal(t, th)
	if len(f.inputs) != 2 || f.inputs[1][0].Content != "original" {
		t.Fatal("cancelled history lost")
	}
}
func TestPlanModeCannotExecuteUnknownCapabilityTool(t *testing.T) {
	et := &echoTool{approve: true}
	f := &fakeModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return chunks(call())
	}}
	th := newThread(t, Config{Model: f, Tools: []tool.BaseTool{et}, PlanMode: true})
	th.SubmitInput(context.Background(), input("go"))
	es := terminal(t, th)
	if es[len(es)-1].Kind != protocol.EventRunFailed || et.count != 0 {
		t.Fatal(es)
	}
}

func TestMultimediaInputAndConsumptionEvent(t *testing.T) {
	f := &fakeModel{stream: func(_ context.Context, _ int, in []*schema.Message) *schema.StreamReader[*schema.Message] {
		return chunks(schema.AssistantMessage("seen", nil))
	}}
	th := newThread(t, Config{Model: f})
	in := input("describe")
	in.Parts = []protocol.Part{{Type: "image", URL: "https://example.test/photo.png", MIMEType: "image/png"}}
	th.SubmitInput(context.Background(), in)
	es := terminal(t, th)
	m := f.inputs[0][0]
	if len(m.UserInputMultiContent) != 2 || m.UserInputMultiContent[1].Image == nil || *m.UserInputMultiContent[1].Image.URL != in.Parts[0].URL {
		t.Fatalf("multimedia was flattened: %+v", m)
	}
	var found bool
	for _, e := range es {
		if e.Kind == protocol.EventInputConsumed {
			found = e.Text == "describe" && len(e.MessageIDs) == 1 && e.MessageIDs[0] == in.ID
		}
	}
	if !found {
		t.Fatal("consumption event lost user text/correlation")
	}
}

func TestReloadRefreshesBootstrapPromptAndPreservesCompaction(t *testing.T) {
	store := &memoryStore{history: []*schema.Message{schema.SystemMessage("old memory"), schema.SystemMessage("Earlier conversation summary: useful"), schema.UserMessage("prior")}}
	f := &fakeModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return chunks(schema.AssistantMessage("ok", nil))
	}}
	th := newThread(t, Config{Model: f, History: store, SystemPrompt: "fresh memory and skills"})
	th.SubmitInput(context.Background(), input("continue"))
	terminal(t, th)
	if f.inputs[0][0].Content != "fresh memory and skills" || f.inputs[0][1].Content != "Earlier conversation summary: useful" {
		t.Fatal("bootstrap prompt stale or compaction overwritten")
	}
}
func TestCancelledCheckpointHistoryRepairsPairsForNewRun(t *testing.T) {
	store := &memoryStore{history: []*schema.Message{schema.UserMessage("previous"), call()}}
	f := &fakeModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return chunks(schema.AssistantMessage("ok", nil))
	}}
	th := newThread(t, Config{Model: f, History: store})
	th.SubmitInput(context.Background(), input("new task"))
	terminal(t, th)
	m := f.inputs[0]
	if len(m) != 4 || m[2].Role != schema.Tool || m[2].ToolCallID != "call1" || m[3].Content != "new task" {
		t.Fatal("orphaned tool call not repaired", m)
	}
}

func TestTokenEventsAccumulateAcrossModelCalls(t *testing.T) {
	f := &fakeModel{stream: func(_ context.Context, i int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		m := call()
		if i > 0 {
			m = schema.AssistantMessage("done", nil)
		}
		m.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
		return chunks(m)
	}}
	th := newThread(t, Config{Model: f, Tools: []tool.BaseTool{&echoTool{}}})
	th.SubmitInput(context.Background(), input("go"))
	es := terminal(t, th)
	var totals []int
	for _, e := range es {
		if e.Kind == protocol.EventTokens {
			var u struct {
				Total int `json:"total_tokens"`
			}
			if err := json.Unmarshal(e.Data, &u); err != nil {
				t.Fatal(err)
			}
			totals = append(totals, u.Total)
		}
	}
	if len(totals) != 2 || totals[0] != 5 || totals[1] != 10 {
		t.Fatal(totals)
	}
}

func TestRedeliveryDoesNotDuplicatePersistedUserInput(t *testing.T) {
	store := &memoryStore{}
	in := input("once")
	f := &fakeModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return chunks(schema.AssistantMessage("done", nil))
	}}
	th := newThread(t, Config{Model: f, History: store})
	th.SubmitInput(context.Background(), in)
	terminal(t, th)
	th.Close()
	th2 := newThread(t, Config{Model: f, History: store})
	th2.SubmitInput(context.Background(), in)
	terminal(t, th2)
	h, _ := store.Load(context.Background())
	count := 0
	for _, m := range h {
		if m.Role == schema.User && m.Content == "once" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("redelivery duplicated user input", count)
	}
}

func TestClarificationReadableOptionsAndResumeOnNewThread(t *testing.T) {
	ctx := context.Background()
	fs, e := backends.NewFilesystem(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	var ask tool.BaseTool
	for _, tt := range fs.Tools() {
		info, _ := tt.Info(ctx)
		if info.Name == "ask_user" {
			ask = tt
			js, e := info.ParamsOneOf.ToJSONSchema()
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(js)
			if !strings.Contains(string(raw), `"options"`) {
				t.Fatal("clarification choices missing from model tool schema")
			}
		}
	}
	if ask == nil {
		t.Fatal("ask_user unavailable")
	}
	f := &fakeModel{stream: func(_ context.Context, i int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if i == 0 {
			return chunks(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "question", Function: schema.FunctionCall{Name: "ask_user", Arguments: `{"question":"Which format?","options":["JSON","YAML"]}`}}}})
		}
		return chunks(schema.AssistantMessage("using YAML", nil))
	}}
	store := &memoryStore{}
	cfg := Config{Model: f, Tools: []tool.BaseTool{ask}, History: store, Checkpoints: store}
	th := newThread(t, cfg)
	run, _ := th.SubmitInput(ctx, input("export"))
	events := terminal(t, th)
	last := events[len(events)-1]
	if last.Kind != protocol.EventBlocked || last.Block == nil || last.Block.Question != "Which format?" || len(last.Block.Options) != 2 || last.Block.Options[1] != "YAML" {
		t.Fatalf("unreadable clarification: %+v", last)
	}
	th.Close()
	next := newThread(t, cfg)
	id, e := next.ResumeRun(ctx, protocol.Input{ID: "answer", Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: run, CheckpointID: last.Block.CheckpointID, InterruptID: last.Block.InterruptID, Answer: "YAML", Approved: false}})
	if e != nil || id != run {
		t.Fatal(id, e)
	}
	events = terminal(t, next)
	if events[len(events)-1].Kind != protocol.EventRunCompleted {
		t.Fatal(events)
	}
	messages := f.inputs[1]
	if messages[len(messages)-1].Role != schema.Tool || messages[len(messages)-1].Content != "YAML" {
		t.Fatal("clarification answer incorrectly treated as approval denial")
	}
}
