package graph

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type sequenceModel struct {
	calls     int
	inputs    [][]*schema.Message
	responses [][]*schema.Message
}

func (m *sequenceModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *sequenceModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected non-stream model call")
}
func (m *sequenceModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	index := m.calls
	m.calls++
	if index >= len(m.responses) {
		return nil, fmt.Errorf("unexpected model call %d", index)
	}
	return schema.StreamReaderFromArray(m.responses[index]), nil
}
func TestRun_StreamAndRunUseSameExecutionPath(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("hel", nil), schema.AssistantMessage("lo", nil)}}}
			a, err := New(context.Background(), WithConfig(&Config{Model: m}))
			if err != nil {
				t.Fatal(err)
			}
			var text string
			if streaming {
				sr, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("input")})
				if err != nil {
					t.Fatal(err)
				}
				defer sr.Close()
				for {
					chunk, err := sr.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					text += chunk.Content
				}
			} else {
				message, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("input")})
				if err != nil {
					t.Fatal(err)
				}
				text = message.Content
			}
			if text != "hello" || m.calls != 1 {
				t.Fatalf("text=%q calls=%d", text, m.calls)
			}
			if len(a.conversation.History(context.Background())) != 2 {
				t.Fatal("missing input or assistant history")
			}
		})
	}
}
func TestRun_ModelToolModel(t *testing.T) {
	ctx := context.Background()
	call := schema.ToolCall{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{call})}, {schema.AssistantMessage("done", nil)}}}
	tool := &countingTool{}
	a, err := New(ctx, WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "done" || m.calls != 2 || tool.count.Load() != 1 {
		t.Fatalf("out=%v calls=%d tool=%d", out, m.calls, tool.count.Load())
	}
	if len(m.inputs[1]) != 3 || m.inputs[1][2].Role != schema.Tool || m.inputs[1][2].ToolCallID != "call" {
		t.Fatalf("tool result not in next context: %v", m.inputs[1])
	}
}
func TestRun_ReturnDirectDoesNotCallModelAgain(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "direct"}}})}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: &countingTool{}, ReturnDirect: true}}, ContinueAfterModel: func(context.Context) (bool, error) {
		t.Error("ReturnDirect consulted model continuation callback")
		return true, nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "direct" || m.calls != 1 {
		t.Fatalf("result=%v modelcalls=%d", out, m.calls)
	}
}

type checkpointMemory struct {
	values map[string][]byte
	fail   bool
}

func (s *checkpointMemory) Get(_ context.Context, id string) ([]byte, bool, error) {
	value, ok := s.values[id]
	return value, ok, nil
}
func (s *checkpointMemory) Set(_ context.Context, id string, value []byte) error {
	if s.fail {
		return fmt.Errorf("checkpoint save failed")
	}
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[id] = append([]byte(nil), value...)
	return nil
}
func TestRun_ApprovalDenyNeverExecutesTool(t *testing.T) {
	ctx := context.Background()
	store := &checkpointMemory{}
	tool := &countingTool{}
	model := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "approved-call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}, {schema.AssistantMessage("denied acknowledged", nil)}}}
	cfg := Config{Model: model, RunID: "run", ThreadID: "thread", CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: tool, RequiresApproval: true}}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("do it")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatalf("expected checkpointed interruption, got %v", err)
	}
	if len(info.InterruptContexts) != 1 {
		t.Fatalf("interrupt contexts: %+v", info)
	}
	approval, ok := info.InterruptContexts[0].Info.(*tools.ApprovalInfo)
	if !ok || approval.CallID != "approved-call" {
		t.Fatalf("approval lost tool identity: %+v", info.InterruptContexts[0].Info)
	}
	cfg.Conversation = first.conversation
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved-call", Approved: false}}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "denied acknowledged" || tool.count.Load() != 0 || model.calls != 2 {
		t.Fatalf("out=%v tool=%d model=%d", out, tool.count.Load(), model.calls)
	}
}

func TestRun_FilesystemWriteRequiresApprovalWithoutExplicitPolicy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := &checkpointMemory{}
	model := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "write", Type: "function", Function: schema.FunctionCall{Name: "write_file", Arguments: `{"path":"result.txt","content":"approved"}`}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	cfg := Config{ThreadID: "thread", RunID: "run", Model: model, CheckpointStore: store,
		Workspace:        backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true}),
		FilesystemConfig: &FilesystemConfig{WorkDir: root, DisableExecute: true, DisableApplyPatch: true}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("write")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		descriptor, found := first.registry.Lookup("write_file")
		t.Fatalf("expected approval interrupt, got %+v: %v; tool found=%v requires_approval=%v history=%v", info, err, found, descriptor.RequiresApproval, first.conversation.History(ctx))
	}
	if _, err := os.Stat(filepath.Join(root, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("write ran before approval: %v", err)
	}
	cfg.Conversation = first.conversation
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{
		info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "write", Approved: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "result.txt"))
	if err != nil || string(content) != "approved" {
		t.Fatalf("write after approval: %q, %v", content, err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want 2", model.calls)
	}
}
func TestCheckpoint_SaveFailureDoesNotPublishBlocked(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, CheckpointStore: &checkpointMemory{fail: true}, ToolDescriptors: []tools.Descriptor{{Tool: &countingTool{}, RequiresApproval: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("input")}, WithCheckpointID("checkpoint"))
	if err == nil {
		t.Fatal("checkpoint failure swallowed")
	}
	if _, ok := compose.ExtractInterruptInfo(err); ok {
		t.Fatal("failed checkpoint published as resumable interruption")
	}
	if a.state.Phase != types.PhaseFailed {
		t.Fatalf("failed checkpoint left phase %s", a.state.Phase)
	}
	if m.calls != 0 {
		t.Fatal("model ran before the initial checkpoint was durable")
	}
}

type namedCountingTool struct {
	name  string
	count int
}

func (t *namedCountingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}
func (t *namedCountingTool) InvokableRun(context.Context, string, ...einotool.Option) (string, error) {
	t.count++
	return t.name, nil
}
func TestRun_ResumeDoesNotRepeatCompletedTool(t *testing.T) {
	ctx := context.Background()
	store := &checkpointMemory{}
	firstTool := &namedCountingTool{name: "first"}
	approvalTool := &namedCountingTool{name: "approval"}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "1", Type: "function", Function: schema.FunctionCall{Name: "first", Arguments: "{}"}}, {ID: "2", Type: "function", Function: schema.FunctionCall{Name: "approval", Arguments: "{}"}}})}, {schema.AssistantMessage("done", nil)}}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: firstTool}, {Tool: approvalTool, RequiresApproval: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("input")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	if firstTool.count != 1 || approvalTool.count != 0 {
		t.Fatal("wrong pre-interrupt side effects")
	}
	cfg.Conversation = a.conversation
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if firstTool.count != 1 || approvalTool.count != 1 {
		t.Fatalf("replayed tools first=%d approved=%d", firstTool.count, approvalTool.count)
	}
	replay, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close(ctx)
	_, err = replay.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err == nil || firstTool.count != 1 || approvalTool.count != 1 || m.calls != 2 {
		t.Fatalf("completed checkpoint replayed: err=%v first=%d approved=%d model=%d", err, firstTool.count, approvalTool.count, m.calls)
	}
}

type eagerModel struct {
	toolStarted <-chan struct{}
	calls       int
}

func (m *eagerModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return m, nil }
func (m *eagerModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}
func (m *eagerModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	if m.calls == 2 {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		index := 9
		writer.Send(schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Index: &index, Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}}), nil)
		select {
		case <-m.toolStarted:
		case <-ctx.Done():
			writer.Send(nil, ctx.Err())
		}
	}()
	return reader, nil
}
func TestRun_EagerExecutesBeforeModelStreamEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tool := &countingTool{started: make(chan struct{})}
	m := &eagerModel{toolStarted: tool.started}
	starts := 0
	a, err := New(ctx, WithConfig(&Config{Model: m, EnableStreamToolCall: true, ToolDescriptors: []tools.Descriptor{{Tool: tool, ParallelSafe: true}}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "tool_start" {
			starts++
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || tool.count.Load() != 1 || starts != 1 {
		t.Fatalf("result=%v count=%d starts=%d", result, tool.count.Load(), starts)
	}
}

type cancelModel struct {
	started chan struct{}
	stopped chan struct{}
}

func (m *cancelModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *cancelModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}
func (m *cancelModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	close(m.started)
	<-ctx.Done()
	close(m.stopped)
	return nil, ctx.Err()
}
func TestRun_CancelAndConsumerCloseReleaseResources(t *testing.T) {
	for _, consumerClose := range []bool{false, true} {
		t.Run(fmt.Sprint(consumerClose), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := &cancelModel{started: make(chan struct{}), stopped: make(chan struct{})}
			a, err := New(ctx, WithModel(m))
			if err != nil {
				t.Fatal(err)
			}
			stream, err := a.Stream(ctx, []*schema.Message{schema.UserMessage("wait")})
			if err != nil {
				t.Fatal(err)
			}
			<-m.started
			if consumerClose {
				stream.Close()
			} else {
				cancel()
				defer stream.Close()
			}
			select {
			case <-m.stopped:
			case <-time.After(time.Second):
				t.Fatal("model remained active after stream close/cancel")
			}
			cleanup, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
			defer cancelCleanup()
			if err := a.Close(cleanup); err != nil {
				t.Fatal(err)
			}
		})
	}
}
