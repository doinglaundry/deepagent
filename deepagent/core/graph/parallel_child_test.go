package graph

import (
	"context"
	"sync"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type parallelChildModel struct {
	mu      sync.Mutex
	started int
	both    chan struct{}
}

func (m *parallelChildModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (*parallelChildModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("must stream")
}
func (m *parallelChildModel) Stream(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	for _, msg := range messages {
		if msg.Role == schema.Tool {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("child done", nil)}), nil
		}
	}
	name := ""
	for _, msg := range messages {
		if msg.Role == schema.User {
			name = msg.Content
		}
	}
	m.mu.Lock()
	m.started++
	if m.started == 2 {
		close(m.both)
	}
	m.mu.Unlock()
	select {
	case <-m.both:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "approval-" + name, Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}), nil
}
func TestChildAgent_ParallelApprovalsResumeTogether(t *testing.T) {
	ctx := context.Background()
	childModel := &parallelChildModel{both: make(chan struct{})}
	counter := &countingTool{}
	task := tools.NewTaskTool(NewChildRunner(Config{Model: childModel, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: true}}}))
	parentModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{
		{ID: "a", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"a"}`}},
		{ID: "b", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"b"}`}},
	})}, {schema.AssistantMessage("parent done", nil)}}}
	cfg := Config{Model: parentModel, RunID: "run", DisableSubAgent: true, Parallelism: 2, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.Descriptor{{Tool: task, ParallelSafe: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("delegate")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 2 {
		t.Fatalf("expected two child approvals: %v", err)
	}
	answers := map[string]any{}
	for _, interrupt := range info.InterruptContexts {
		approval := interrupt.Info.(*tools.ApprovalInfo)
		answers[interrupt.ID] = &tools.ApprovalResult{CallID: approval.CallID, Approved: true}
	}
	cfg.Conversation = a.conversation
	resumed, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := resumed.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(answers))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "parent done" || counter.count.Load() != 2 || parentModel.calls != 2 || childModel.started != 2 {
		t.Fatalf("out=%v tools=%d parent=%d children=%d", out, counter.count.Load(), parentModel.calls, childModel.started)
	}
}

// Simulate the former child address format while producing a real Eino
// checkpoint. The resumed execution uses the normal current ChildRunner.
type legacyAddressChild struct{ tools.ChildRunner }

func (r legacyAddressChild) Run(ctx context.Context, req tools.ChildRequest, emit types.ModelChunkSink) (*schema.Message, error) {
	executor := ctx.Value(toolExecutorKey{}).(*toolExecutor)
	id := ctx.Value(toolCallIDKey{}).(string)
	executor.childCheckpoint(id).addressed = false
	return r.ChildRunner.Run(ctx, req, emit)
}

func TestChildAgent_PreCallAddressCheckpointResumes(t *testing.T) {
	ctx := context.Background()
	counter := &countingTool{}
	childModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "approval", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("child done", nil)},
	}}
	runner := NewChildRunner(Config{Model: childModel, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: true}}})
	parentModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "child", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"work"}`}}})},
		{schema.AssistantMessage("parent done", nil)},
	}}
	cfg := Config{Model: parentModel, DisableSubAgent: true, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.Descriptor{{Tool: tools.NewTaskTool(legacyAddressChild{runner})}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("delegate")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		t.Fatalf("missing approval: %v", err)
	}
	if _, ok := a.state.Extensions["child_checkpoint_address/child"]; ok {
		t.Fatal("legacy snapshot has new address marker")
	}
	cfg.Conversation = a.conversation
	cfg.ToolDescriptors = []tools.Descriptor{{Tool: tools.NewTaskTool(runner)}}
	resumed, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := resumed.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approval", Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "parent done" || counter.count.Load() != 1 || childModel.calls != 2 {
		t.Fatalf("out=%v count=%d childCalls=%d", out, counter.count.Load(), childModel.calls)
	}
}
