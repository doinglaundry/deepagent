package graph

import (
	"context"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	agents, err := LoadSubAgents(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, SubAgents: agents}))
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
	_, err = New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, SubAgents: append(agents, &SubAgent{Name: "reviewer"})}))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate configured child accepted: %v", err)
	}
	directModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("direct", nil)}}}
	out, err := NewChildRunner(Config{Model: directModel, SubAgents: agents}).Run(context.Background(), tools.ChildRequest{Name: "reviewer", Prompt: "direct task"}, nil)
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
	a, err := New(context.Background(), WithConfig(&Config{Model: m, SubAgents: []*SubAgent{{Name: "general-purpose"}}, ToolDescriptors: []tools.Descriptor{{Tool: tool}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
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
	})
	result, err := runner.Run(context.Background(), tools.ChildRequest{Name: "reviewer", Prompt: "selected context\nreview task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "reviewed" || len(m.inputs) != 1 || len(m.inputs[0]) != 2 {
		t.Fatalf("result=%v inputs=%v", result, m.inputs)
	}
	if m.inputs[0][0].Content != "review carefully" || m.inputs[0][1].Content != "selected context\nreview task" {
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
	runner := NewChildRunner(Config{SubAgents: []*SubAgent{{Name: "general-purpose"}}, Model: model, RunID: "parent-run", MaxModelCalls: 1, Conversation: parentHistory, ToolDescriptors: []tools.Descriptor{{Tool: tool}}})
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
	a, err := New(context.Background(), WithModel(m), WithSubAgents(&SubAgent{Name: "general-purpose"}))
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

type boundedChildRunner struct {
	mu           sync.Mutex
	active, peak int
	started      chan string
	release      chan struct{}
}

func (r *boundedChildRunner) Run(ctx context.Context, req tools.ChildRequest, _ types.ModelChunkSink) (*schema.Message, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.peak {
		r.peak = r.active
	}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.active--; r.mu.Unlock() }()
	r.started <- req.Prompt
	select {
	case <-r.release:
		return schema.AssistantMessage(req.Prompt, nil), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestChildAgent_ConcurrencyLimitQueuesEveryTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := &boundedChildRunner{started: make(chan string, 5), release: make(chan struct{}, 5)}
	registry, err := tools.NewRegistry(ctx, []tools.Descriptor{{Tool: tools.NewTaskTool(runner), ParallelSafe: true}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor("run", registry, 2, nil)
	var calls []types.ToolCall
	for _, i := range []int{4, 2, 0, 3, 1} {
		calls = append(calls, types.ToolCall{ID: fmt.Sprint(i), Index: i, Name: "task", Arguments: fmt.Sprintf(`{"description":"task-%d"}`, i)})
	}
	done := make(chan struct {
		results []types.ToolResult
		err     error
	}, 1)
	go func() {
		results, err := executor.executeBatch(ctx, calls, nil)
		done <- struct {
			results []types.ToolResult
			err     error
		}{results, err}
	}()
	seen := map[string]bool{}
	for _, size := range []int{2, 2, 1} {
		for range size {
			select {
			case name := <-runner.started:
				if seen[name] {
					t.Fatalf("duplicate task %s", name)
				}
				seen[name] = true
			case <-ctx.Done():
				t.Fatal("queued task was dropped or deadlocked")
			}
		}
		for range size {
			runner.release <- struct{}{}
		}
	}
	select {
	case out := <-done:
		if out.err != nil || len(out.results) != 5 {
			t.Fatalf("results=%v err=%v", out.results, out.err)
		}
		for i, result := range out.results {
			if result.CallID != fmt.Sprint(i) || result.Content != fmt.Sprintf("task-%d", i) {
				t.Fatalf("result order lost: %+v", out.results)
			}
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.peak != 2 || runner.active != 0 || len(seen) != 5 {
		t.Fatalf("peak=%d active=%d seen=%v", runner.peak, runner.active, seen)
	}
}

func writeSpec(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SUBAGENT.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestLoadSubAgentsConfigurationAndOrdering(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, "z", "name: reviewer\nsystem_prompt: review carefully\nmax_steps: 12\nread_only: true\nenable_filesystem: true\ntools: [read_file]\n")
	writeSpec(t, root, "a", "system_prompt: analyze\n")
	agents, err := LoadSubAgents(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].Name != "a" || agents[1].Name != "reviewer" {
		t.Fatalf("agents=%+v", agents)
	}
	a := agents[1]
	if a.SystemPrompt != "review carefully" || a.MaxSteps != 12 || !a.ReadOnly || !a.EnableFilesystem || a.EnableWeb {
		t.Fatalf("config=%+v", a)
	}
	if !a.ToolMask(context.Background(), &schema.ToolInfo{Name: "read_file"}) || a.ToolMask(context.Background(), &schema.ToolInfo{Name: "write_file"}) {
		t.Fatal("tool allowlist not applied")
	}
}
func TestLoadSubAgentsRejectsInvalidAndEscapingSpecs(t *testing.T) {
	for _, content := range []string{"system_prompt: x\nunknown: true\n", "system_prompt: x\nmax_steps: -1\n", "name: empty\n", "system_prompt: x\n---\nname: second\n", "system_prompt: x\ntools: [read_file, read_file]\n"} {
		root := t.TempDir()
		writeSpec(t, root, "bad", content)
		if _, err := LoadSubAgents(context.Background(), root); err == nil {
			t.Fatalf("accepted %q", content)
		}
	}
	root, outside := t.TempDir(), t.TempDir()
	writeSpec(t, outside, "external", "system_prompt: outside\n")
	if err := os.Mkdir(filepath.Join(root, "escape"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "external", "SUBAGENT.yaml"), filepath.Join(root, "escape", "SUBAGENT.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSubAgents(context.Background(), root); err == nil {
		t.Fatal("followed escaping config symlink")
	}
}

func TestChildAgent_RequiresExplicitRegistration(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{}
	a, err := New(ctx, WithModel(m))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, exists := a.registry.Lookup("task")
	if exists {
		t.Fatal("task registered without configured subagents")
	}
	runner := NewChildRunner(Config{Model: m})
	_, err = runner.Run(ctx, tools.ChildRequest{Name: "general-purpose", Prompt: "work"}, nil)
	if err == nil || m.calls != 0 {
		t.Fatalf("unconfigured child executed: err=%v calls=%d", err, m.calls)
	}
}
