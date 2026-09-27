package graph

import (
	"context"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"sync"
	"testing"
)

func TestChildAgent_ApprovalResumesNestedGraphOnNewParent(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%v", allow), func(t *testing.T) { testChildApprovalResume(t, allow) })
	}
}

func testChildApprovalResume(t *testing.T, allow bool) {
	ctx := context.Background()
	first, approved := &namedCountingTool{name: "first"}, &namedCountingTool{name: "approved"}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "child-task", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"perform child work"}`}}})},
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "first", Function: schema.FunctionCall{Name: "first", Arguments: "{}"}}, {ID: "approved", Function: schema.FunctionCall{Name: "approved", Arguments: "{}"}}})},
		{schema.AssistantMessage("child done", nil)},
		{schema.AssistantMessage("parent done", nil)},
	}}
	store := &checkpointMemory{}
	cfg := Config{SubAgents: []*SubAgent{{Name: "general-purpose"}}, Model: m, ThreadID: "parent", RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: first}, {Tool: approved, RequiresApproval: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("delegate")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		t.Fatalf("missing child approval: %v %+v", err, info)
	}
	if first.count != 1 || approved.count != 0 || m.calls != 2 {
		t.Fatalf("before resume counts=%d/%d model=%d", first.count, approved.count, m.calls)
	}
	if len(store.values) != 1 {
		t.Fatalf("child created external sidecar: %d entries", len(store.values))
	}
	if len(a.state.Extensions["child_checkpoint/child-task"]) == 0 {
		t.Fatal("child checkpoint not embedded")
	}
	cfg.Conversation = a.conversation
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved", Approved: allow}}))
	if err != nil {
		t.Fatal(err)
	}
	wantApproved := 0
	if allow {
		wantApproved = 1
	}
	if out.Content != "parent done" || m.calls != 4 || first.count != 1 || approved.count != wantApproved {
		t.Fatalf("out=%v model=%d counts=%d/%d phase=%s", out, m.calls, first.count, approved.count, restored.state.Phase)
	}
	if len(restored.state.Extensions["child_checkpoint/child-task"]) != 0 {
		t.Fatal("completed child retained stale checkpoint")
	}
	childInput := m.inputs[2]
	foundPrompt, foundAssistant, foundFirst, foundApproved := false, false, false, false
	for _, message := range childInput {
		if message.Role == schema.User && message.Content == "perform child work" {
			foundPrompt = true
		}
		if len(message.ToolCalls) == 2 {
			foundAssistant = true
		}
		if message.Role == schema.Tool && message.ToolCallID == "first" {
			foundFirst = true
		}
		if message.Role == schema.Tool && message.ToolCallID == "approved" {
			foundApproved = true
		}
	}
	if !foundPrompt || !foundAssistant || !foundFirst || !foundApproved {
		t.Fatalf("restored child lost conversation: %+v", childInput)
	}
}

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
	task := tools.NewTaskTool(NewChildRunner(Config{SubAgents: []*SubAgent{{Name: "general-purpose"}}, Model: childModel, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: true}}}))
	parentModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{
		{ID: "a", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"a"}`}},
		{ID: "b", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"b"}`}},
	})}, {schema.AssistantMessage("parent done", nil)}}}
	cfg := Config{Model: parentModel, RunID: "run", Parallelism: 2, CheckpointStore: &checkpointMemory{}, Policy: tools.PolicyFunc(func(context.Context, types.ToolCall, tools.Descriptor) (tools.Decision, error) {
		return tools.Decision{Action: tools.Allow}, nil
	}), ToolDescriptors: []tools.Descriptor{{Tool: task, ParallelSafe: true}}}
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
	ids := make([]string, 0, len(info.InterruptContexts))
	for _, interrupt := range info.InterruptContexts {
		approval := interrupt.Info.(*tools.ApprovalInfo)
		ids = append(ids, interrupt.ID)
		answers[interrupt.ID] = &tools.ApprovalResult{CallID: approval.CallID, Approved: true}
	}
	cfg.Conversation = a.conversation
	resumed, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := resumed.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResume(ids...), WithResumeData(answers))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "parent done" || counter.count.Load() != 2 || parentModel.calls != 2 || childModel.started != 2 {
		t.Fatalf("out=%v tools=%d parent=%d children=%d", out, counter.count.Load(), parentModel.calls, childModel.started)
	}
}
