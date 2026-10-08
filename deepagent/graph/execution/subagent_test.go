package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestChildAgent_ApprovalResumesNestedGraphOnNewParent(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%v", allow), func(t *testing.T) { testChildApprovalResume(t, allow) })
	}
}

func TestChildAgent_ParallelApprovalsResumeTogether(t *testing.T) {
	ctx := context.Background()
	childModel := &parallelChildModel{both: make(chan struct{})}
	counter := &countingTool{}
	task := tools.NewTaskTool(NewChildRunner(Config{SubAgents: []*SubAgent{{Name: "general-purpose"}}, Model: childModel, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}))
	task.ParallelSafe = true
	parentModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{
		{ID: "a", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"a"}`}},
		{ID: "b", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"b"}`}},
	})}, {schema.AssistantMessage("parent done", nil)}}}
	config := Config{Model: parentModel, RunID: "run", Parallelism: 2, CheckpointStore: &checkpointMemory{}, Policy: tools.PolicyFunc(func(context.Context, types.ToolCall, tools.ToolDescriptor) (tools.Decision, error) {
		return tools.Decision{Action: tools.Allow}, nil
	}), ToolDescriptors: []tools.ToolDescriptor{task}}
	graph, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("delegate")}, WithCheckpointID("checkpoint"))
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
	config.Conversation = graph.conversation
	resumed, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	out, err := resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResume(ids...), WithResumeData(answers))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "parent done" || counter.count.Load() != 2 || parentModel.calls != 2 || childModel.started != 2 {
		t.Fatalf("out=%v tools=%d parent=%d children=%d", out, counter.count.Load(), parentModel.calls, childModel.started)
	}
}

func TestCheckpoint_ChildConversationRestoresProviderUsage(t *testing.T) {
	ctx := context.Background()
	counter := func(messages []*messagepkg.Message) int { return len(messages) * 3 }
	initial := conversation.New("", nil, nil, counter)
	fresh := conversation.New("", nil, nil, counter)
	reply := schema.AssistantMessage("", []schema.ToolCall{{ID: "approval", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})
	reply.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 400, CompletionTokens: 10, TotalTokens: 410}}
	chatModel := &childUsageModel{sequenceModel: sequenceModel{responses: [][]*schema.Message{{reply}, {schema.AssistantMessage("done", nil)}}}, check: func() {
		usage := fresh.GetContextUsage()
		if usage.Source != types.ContextUsageSourceModelUsage || usage.LastModelTotal != 410 || usage.CurrentTotal != 413 || usage.EstimatedAfterLastModel != 3 {
			t.Errorf("lost provider baseline: %+v", usage)
		}
	}}
	config := Config{Depth: 1, RunID: "child-run", Model: chatModel, Conversation: initial, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, RequiresApproval: true}}}
	graph, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("work")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	config.Conversation = fresh
	restored, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if chatModel.calls != 2 {
		t.Fatalf("model=%d", chatModel.calls)
	}
}

func TestChildAgent_DirectoryConfigurationReachesTaskGraph(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "reviewer")
	mkdirErr := os.Mkdir(dir, 0700)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	writeErr := os.WriteFile(filepath.Join(dir, "SUBAGENT.yaml"), []byte("system_prompt: review loaded configuration\nread_only: true\nmax_steps: 12\n"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	chatModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "task", Function: schema.FunctionCall{Name: "task", Arguments: `{"subagent_type":"reviewer","description":"review task"}`}}})},
		{schema.AssistantMessage("review done", nil)},
		{schema.AssistantMessage("parent done", nil)},
	}}
	agents, err := LoadSubAgents(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, SubAgents: agents}))
	if err != nil {
		t.Fatal(err)
	}
	infos, err := graph.toolSet.GetToolInfos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(infos)
	if err != nil || !strings.Contains(string(raw), "reviewer") {
		t.Fatalf("task schema does not advertise loaded agent: %s %v", raw, err)
	}
	_, executeErr := graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("delegate")})
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	if len(chatModel.inputs) != 3 || len(chatModel.inputs[1]) != 2 || chatModel.inputs[1][0].Content != "review loaded configuration" || chatModel.inputs[1][1].Content != "review task" {
		t.Fatalf("loaded child config not used: %+v", chatModel.inputs)
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
	chatModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "task-call", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"child task"}`}}})},
		{schema.AssistantMessage("internal progress", []schema.ToolCall{{ID: "child-tool", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("fi", nil), schema.AssistantMessage("nal", nil)},
		{schema.AssistantMessage("parent done", nil)},
	}}
	var chunks []string
	tool := &countingTool{}
	graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, SubAgents: []*SubAgent{{Name: "general-purpose"}}, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		chunk, ok := event.Data.(types.ToolCallOutputChunkPayload)
		if ok && chunk.CallID == "task-call" {
			chunks = append(chunks, chunk.Chunk)
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("delegate")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "parent done" || chatModel.calls != 4 || tool.count.Load() != 1 || len(chunks) != 2 || strings.Join(chunks, "") != "final" {
		t.Fatalf("out=%v models=%d tools=%d chunks=%v", out, chatModel.calls, tool.count.Load(), chunks)
	}
	last := chatModel.inputs[3][len(chatModel.inputs[3])-1]
	if last.Role != schema.Tool || last.Content != "final" {
		t.Fatalf("child intermediate text leaked: %+v", last)
	}
}

func TestChildAgent_NamedCapabilitiesAndContext(t *testing.T) {
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("reviewed", nil)}}}
	runner := NewChildRunner(Config{
		Model: chatModel,
		// This configuration is intentionally unusable without a backend. The
		// named child's disabled filesystem must prevent it from being assembled.
		FilesystemConfig: &FilesystemConfig{},
		SubAgents:        []*SubAgent{{Name: "reviewer", SystemPrompt: "review carefully"}},
	})
	result, err := runner.Run(context.Background(), tools.ChildRequest{Name: "reviewer", Prompt: "selected context\nreview task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "reviewed" || len(chatModel.inputs) != 1 || len(chatModel.inputs[0]) != 2 {
		t.Fatalf("result=%v inputs=%v", result, chatModel.inputs)
	}
	if chatModel.inputs[0][0].Content != "review carefully" || chatModel.inputs[0][1].Content != "selected context\nreview task" {
		t.Fatalf("child context=%v", chatModel.inputs[0])
	}
}

func TestChildAgent_UsesSameGraphWithIndependentBudget(t *testing.T) {
	ctx := context.Background()
	parentHistory := conversation.New("parent", nil, nil, nil)
	addHistoryErr := parentHistory.AddHistory(ctx, "parent-run", messagepkg.NewUserMessage("private parent context"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	model := &childModel{}
	tool := &countingTool{}
	runner := NewChildRunner(Config{SubAgents: []*SubAgent{{Name: "general-purpose"}}, Model: model, RunID: "parent-run", MaxModelCalls: 1, Conversation: parentHistory, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}})
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
	if len(parentHistory.GetHistory(ctx)) != 1 {
		t.Fatal("child mutated parent conversation")
	}
}

func TestChildAgent_TaskIsRegisteredOnParentGraph(t *testing.T) {
	chatModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "task-call", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"child task"}`}}})},
		{schema.AssistantMessage("child result", nil)},
		{schema.AssistantMessage("parent result", nil)},
	}}
	graph, err := New(context.Background(), WithModel(chatModel), WithSubAgents(&SubAgent{Name: "general-purpose"}))
	if err != nil {
		t.Fatal(err)
	}
	_, ok := graph.toolSet.GetToolDescriptor("task")
	if !ok {
		t.Fatal("task is not registered")
	}
	result, err := graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("delegate")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "parent result" || chatModel.calls != 3 {
		t.Fatalf("result=%v calls=%d", result, chatModel.calls)
	}
	if len(chatModel.inputs[1]) != 1 || chatModel.inputs[1][0].Content != "child task" {
		t.Fatal("child inherited parent history")
	}
	got := chatModel.inputs[2][len(chatModel.inputs[2])-1]
	if got.Role != schema.Tool || got.Content != "child result" {
		t.Fatalf("child output lost: %v", got)
	}
}

func TestChildAgent_ConcurrencyLimitQueuesEveryTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := &boundedChildRunner{started: make(chan string, 5), release: make(chan struct{}, 5)}
	task := tools.NewTaskTool(runner)
	task.ParallelSafe = true
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{task})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor(toolSet, 2, nil)
	var calls []types.ToolCall
	for _, i := range []int{4, 2, 0, 3, 1} {
		calls = append(calls, types.ToolCall{ID: fmt.Sprint(i), Index: i, Name: "task", Arguments: fmt.Sprintf(`{"description":"task-%d"}`, i)})
	}
	done := make(chan struct {
		results []types.ToolResult
		err     error
	}, 1)
	go func() {
		err := executor.executeToolBatch(ctx, calls, nil)
		states := make([]types.ToolCallState, len(calls))
		for i, call := range calls {
			states[i].Call = call
		}
		executor.snapshotToolExecutions(states)
		sort.SliceStable(states, func(i, j int) bool { return states[i].Call.Index < states[j].Call.Index })
		results := make([]types.ToolResult, 0, len(states))
		for _, state := range states {
			if state.Result != nil {
				results = append(results, *state.Result)
			}
		}
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

func TestLoadSubAgentsConfigurationAndOrdering(t *testing.T) {
	root := t.TempDir()
	writeSubAgentSpec(t, root, "z", "name: reviewer\nsystem_prompt: review carefully\nmax_steps: 12\nread_only: true\nenable_filesystem: true\ntools: [read_file]\n")
	writeSubAgentSpec(t, root, "a", "system_prompt: analyze\n")
	agents, err := LoadSubAgents(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].Name != "a" || agents[1].Name != "reviewer" {
		t.Fatalf("agents=%+v", agents)
	}
	subAgent := agents[1]
	if subAgent.SystemPrompt != "review carefully" || subAgent.MaxSteps != 12 || !subAgent.ReadOnly || !subAgent.EnableFilesystem || subAgent.EnableWeb {
		t.Fatalf("config=%+v", subAgent)
	}
	if !subAgent.ToolMask(context.Background(), &schema.ToolInfo{Name: "read_file"}) || subAgent.ToolMask(context.Background(), &schema.ToolInfo{Name: "write_file"}) {
		t.Fatal("tool allowlist not applied")
	}
}

func TestLoadSubAgentsRejectsInvalidAndEscapingSpecs(t *testing.T) {
	for _, content := range []string{"system_prompt: x\nunknown: true\n", "system_prompt: x\nmax_steps: -1\n", "name: empty\n", "system_prompt: x\n---\nname: second\n", "system_prompt: x\ntools: [read_file, read_file]\n"} {
		root := t.TempDir()
		writeSubAgentSpec(t, root, "bad", content)
		_, err := LoadSubAgents(context.Background(), root)
		if err == nil {
			t.Fatalf("accepted %q", content)
		}
	}
	root, outside := t.TempDir(), t.TempDir()
	writeSubAgentSpec(t, outside, "external", "system_prompt: outside\n")
	mkdirErr := os.Mkdir(filepath.Join(root, "escape"), 0700)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	symlinkErr := os.Symlink(filepath.Join(outside, "external", "SUBAGENT.yaml"), filepath.Join(root, "escape", "SUBAGENT.yaml"))
	if symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	_, loadSubAgentsErr := LoadSubAgents(context.Background(), root)
	if loadSubAgentsErr == nil {
		t.Fatal("followed escaping config symlink")
	}
}

func TestChildAgent_RequiresExplicitRegistration(t *testing.T) {
	ctx := context.Background()
	chatModel := &sequenceModel{}
	graph, err := New(ctx, WithModel(chatModel))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	_, exists := graph.toolSet.GetToolDescriptor("task")
	if exists {
		t.Fatal("task registered without configured subagents")
	}
	runner := NewChildRunner(Config{Model: chatModel})
	_, err = runner.Run(ctx, tools.ChildRequest{Name: "general-purpose", Prompt: "work"}, nil)
	if err == nil || chatModel.calls != 0 {
		t.Fatalf("unconfigured child executed: err=%v calls=%d", err, chatModel.calls)
	}
}

func TestChildAgent_CustomPromptRetainsSharedInstructions(t *testing.T) {
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	config := Config{
		Model:     chatModel,
		Prompts:   []*messagepkg.Message{messagepkg.NewSystemMessage("shared operating instructions")},
		SubAgents: []*SubAgent{{Name: "review", SystemPrompt: "review-specific instructions"}},
	}
	_, err := NewChildRunner(config).Run(context.Background(), tools.ChildRequest{Name: "review", Prompt: "inspect"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	shared, child := 0, 0
	for _, message := range chatModel.inputs[0] {
		if message.Content == "shared operating instructions" {
			shared++
		}
		if message.Content == "review-specific instructions" {
			child++
		}
	}
	if shared != 1 || child != 1 || len(config.Prompts) != 1 {
		t.Fatalf("shared=%d child=%d parent prompts=%d", shared, child, len(config.Prompts))
	}
}
