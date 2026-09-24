package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestChildAgent_DirectoryConfigurationReachesTaskGraph(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "reviewer")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SUBAGENT.yaml"), []byte("system_prompt: review loaded configuration\nread_only: true\nmax_steps: 12\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "task", Function: schema.FunctionCall{Name: "task", Arguments: `{"subagent_type":"reviewer","description":"review task"}`}}})},
		{schema.AssistantMessage("review done", nil)},
		{schema.AssistantMessage("parent done", nil)},
	}}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, SubAgentsDirs: []string{root}}))
	if err != nil {
		t.Fatal(err)
	}
	infos, err := a.registry.ModelTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(infos)
	if err != nil || !strings.Contains(string(raw), "reviewer") {
		t.Fatalf("task schema does not advertise loaded agent: %s %v", raw, err)
	}
	if _, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("delegate")}); err != nil {
		t.Fatal(err)
	}
	if len(m.inputs) != 3 || len(m.inputs[1]) != 2 || m.inputs[1][0].Content != "review loaded configuration" || m.inputs[1][1].Content != "review task" {
		t.Fatalf("loaded child config not used: %+v", m.inputs)
	}
	_, err = New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, SubAgentsDirs: []string{root}, SubAgents: []*SubAgent{{Name: "reviewer"}}}))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate configured child accepted: %v", err)
	}
	directModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("direct", nil)}}}
	out, err := NewChildRunner(Config{Model: directModel, SubAgentsDirs: []string{root}}).Run(context.Background(), tools.ChildRequest{Name: "reviewer", Prompt: "direct task"}, nil)
	if err != nil || out.Content != "direct" || directModel.inputs[0][0].Content != "review loaded configuration" {
		t.Fatalf("direct child runner did not load config: out=%v err=%v", out, err)
	}
}

func TestChildAgent_TaskStreamingReturnsOnlyFinalAnswer(t *testing.T) {
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "task-call", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"child task"}`}}})},
		{schema.AssistantMessage("internal progress", []schema.ToolCall{{ID: "child-tool", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("fi", nil), schema.AssistantMessage("nal", nil)},
		{schema.AssistantMessage("parent done", nil)},
	}}
	var chunks []string
	tool := &countingTool{}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, EnableSubAgentTaskStreaming: true, ToolDescriptors: []tools.Descriptor{{Tool: tool}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		if chunk, ok := event.Data.(types.ToolOutputChunk); ok && chunk.Call.ID == "task-call" {
			chunks = append(chunks, chunk.Content)
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("delegate")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "parent done" || m.calls != 4 || tool.count.Load() != 1 || len(chunks) != 2 || strings.Join(chunks, "") != "final" {
		t.Fatalf("out=%v models=%d tools=%d chunks=%v", out, m.calls, tool.count.Load(), chunks)
	}
	last := m.inputs[3][len(m.inputs[3])-1]
	if last.Role != schema.Tool || last.Content != "final" {
		t.Fatalf("child intermediate text leaked: %+v", last)
	}
}

type childModel struct{ inputs [][]*schema.Message }

func (m *childModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return m, nil }
func (m *childModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}

func TestChildAgent_NamedCapabilitiesAndContext(t *testing.T) {
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("reviewed", nil)}}}
	runner := NewChildRunner(Config{
		Model: m,
		// This configuration is intentionally unusable without a backend. The
		// named child's disabled filesystem must prevent it from being assembled.
		FilesystemConfig: &FilesystemConfig{},
		SubAgents:        []*SubAgent{{Name: "reviewer", SystemPrompt: "review carefully"}},
		SubAgentContextInjector: func(_ context.Context, name string) ([]*schema.Message, error) {
			if name != "reviewer" {
				t.Fatalf("injected context for %q", name)
			}
			return []*schema.Message{schema.UserMessage("selected context")}, nil
		},
	})
	result, err := runner.Run(context.Background(), tools.ChildRequest{Name: "reviewer", Prompt: "review task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "reviewed" || len(m.inputs) != 1 || len(m.inputs[0]) != 3 {
		t.Fatalf("result=%v inputs=%v", result, m.inputs)
	}
	if m.inputs[0][0].Content != "review carefully" || m.inputs[0][1].Content != "selected context" || m.inputs[0][2].Content != "review task" {
		t.Fatalf("child context=%v", m.inputs[0])
	}
}
func (m *childModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if input[len(input)-1].Role == schema.Tool {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("child done", nil)}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}), nil
}
func TestChildAgent_UsesSameGraphWithIndependentBudget(t *testing.T) {
	ctx := context.Background()
	parentHistory := conversation.New("parent", nil, nil, nil)
	if err := parentHistory.AddHistory(ctx, "parent-run", schema.UserMessage("private parent context")); err != nil {
		t.Fatal(err)
	}
	model := &childModel{}
	tool := &countingTool{}
	runner := NewChildRunner(Config{Model: model, RunID: "parent-run", MaxModelCalls: 1, Conversation: parentHistory, ToolDescriptors: []tools.Descriptor{{Tool: tool}}})
	_, err := runner.Run(ctx, tools.ChildRequest{Name: "general-purpose", Prompt: "first child", MaxModelCalls: 1}, nil)
	if err == nil || !strings.Contains(err.Error(), "maximum model calls") {
		t.Fatalf("child budget not enforced: %v", err)
	}
	result, err := runner.Run(ctx, tools.ChildRequest{Name: "general-purpose", Prompt: "second child", MaxModelCalls: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "child done" || tool.count.Load() != 2 {
		t.Fatalf("result=%v toolcalls=%d", result, tool.count.Load())
	}
	if len(model.inputs) != 3 || len(model.inputs[1]) != 1 || model.inputs[1][0].Content != "second child" {
		t.Fatalf("child history or budget leaked: %v", model.inputs)
	}
	if len(parentHistory.History(ctx)) != 1 {
		t.Fatal("child mutated parent conversation")
	}
}

func TestChildAgent_TaskIsRegisteredOnParentGraph(t *testing.T) {
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "task-call", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"child task"}`}}})},
		{schema.AssistantMessage("child result", nil)},
		{schema.AssistantMessage("parent result", nil)},
	}}
	a, err := New(context.Background(), WithModel(m))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.registry.Lookup("task"); !ok {
		t.Fatal("task is not registered")
	}
	result, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("delegate")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "parent result" || m.calls != 3 {
		t.Fatalf("result=%v calls=%d", result, m.calls)
	}
	if len(m.inputs[1]) != 1 || m.inputs[1][0].Content != "child task" {
		t.Fatal("child inherited parent history")
	}
	if got := m.inputs[2][len(m.inputs[2])-1]; got.Role != schema.Tool || got.Content != "child result" {
		t.Fatalf("child output lost: %v", got)
	}
}
