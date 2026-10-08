package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/skills"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestLoopGuardStopsRepeatedToolsAndIsRunLocal(t *testing.T) {
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	for range 2 {
		chatModel := &sequenceModel{responses: [][]*schema.Message{
			{schema.AssistantMessage("", []schema.ToolCall{{ID: "first", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
			{schema.AssistantMessage("stopping loop", []schema.ToolCall{{ID: "second", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		}}
		tool := &countingTool{}
		graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, EnableEagerTools: true, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ParallelSafe: true}}}))
		if err != nil {
			t.Fatal(err)
		}
		result, err := graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		if result.Content != "stopping loop" || tool.count.Load() != 1 || chatModel.calls != 2 {
			t.Fatalf("result=%v tools=%d model=%d", result, tool.count.Load(), chatModel.calls)
		}
	}
}

func TestLoopGuardRestoresWindowFromCheckpoint(t *testing.T) {
	ctx := context.Background()
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	counter := &countingTool{}
	chatModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "first", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("stopping loop", []schema.ToolCall{{ID: "second", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
	}}
	config := Config{Model: chatModel, RunID: "run", CheckpointStore: &checkpointMemory{}, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}
	first, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)
	_, err = first.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		t.Fatalf("interrupt=%+v err=%v", info, err)
	}
	config.Conversation = first.conversation
	resumed, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(ctx)
	out, err := resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "first", Approved: true}}))
	if err != nil || out == nil || out.Content != "stopping loop" || len(out.ToolCalls) != 0 || counter.count.Load() != 1 || chatModel.calls != 2 {
		t.Fatalf("out=%v err=%v tools=%d model=%d", out, err, counter.count.Load(), chatModel.calls)
	}
}

func TestLoopGuardDistinctCallsExpireFromWindow(t *testing.T) {
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	guard.WindowSize = 2
	chatModel := &sequenceModel{}
	for i, args := range []string{`{"value":1}`, `{"value":2}`, `{"value":1}`} {
		chatModel.responses = append(chatModel.responses, []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: fmt.Sprint(i), Function: schema.FunctionCall{Name: "counter", Arguments: args}}})})
	}
	chatModel.responses = append(chatModel.responses, []*schema.Message{schema.AssistantMessage("done", nil)})
	counter := &countingTool{}
	graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	out, err := graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("go")})
	if err != nil || out == nil || out.Content != "done" || counter.count.Load() != 3 {
		t.Fatalf("out=%v err=%v calls=%d", out, err, counter.count.Load())
	}
}

func TestMiddleware_OrderAndAfterRunOnce(t *testing.T) {
	ctx := context.Background()
	var order []string
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, ReturnDirect: true}}, Middlewares: []middleware.Middleware{&orderedMiddleware{name: "outer", order: &order}, &orderedMiddleware{name: "inner", order: &order}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := graph.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("go")})
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	want := []string{"before:outer", "before:inner", "model:outer", "model:inner", "after:inner", "after:outer"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("middleware order=%v want=%v", order, want)
	}
}

func TestRun_FinalEventFollowsAfterRun(t *testing.T) {
	ctx := context.Background()
	currentMiddleware := &endOrderMiddleware{}
	graph, err := New(ctx, WithConfig(&Config{Model: &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}, Middlewares: []middleware.Middleware{currentMiddleware}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "turn_end" && !currentMiddleware.after {
			t.Error("final event preceded AfterRun")
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := graph.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("input")})
	if executeErr != nil {
		t.Fatal(executeErr)
	}
}

func TestRun_ModelMiddlewareModifiesRequestAndStream(t *testing.T) {
	ctx := context.Background()
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("original", nil)}}}
	currentMiddleware := &modelTransformMiddleware{}
	graph, err := New(ctx, WithModel(chatModel), WithMiddleware(currentMiddleware))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	out, err := graph.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("input")})
	if err != nil {
		t.Fatal(err)
	}
	if currentMiddleware.before != 1 || currentMiddleware.after != 1 || out.Content != "rewritten" || chatModel.inputs[0][0].Content != "middleware prompt" {
		t.Fatalf("middleware not applied: before=%d after=%d output=%v inputs=%v", currentMiddleware.before, currentMiddleware.after, out, chatModel.inputs)
	}
	history := graph.conversation.GetHistory(ctx)
	if history[len(history)-1].Content != "rewritten" {
		t.Fatal("history bypassed middleware")
	}
}

func TestRun_ModelMiddlewareErrorStopsModel(t *testing.T) {
	want := errors.New("before model failed")
	chatModel := &sequenceModel{}
	graph, err := New(context.Background(), WithModel(chatModel), WithMiddleware(&modelTransformMiddleware{failure: want}))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	_, err = graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("input")})
	if !errors.Is(err, want) || chatModel.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, chatModel.calls)
	}
}

func TestDuplicateStatefulMiddlewareNamesRejectedBeforeModelCall(t *testing.T) {
	tests := []struct {
		name        string
		middlewares []middleware.Middleware
	}{
		{name: "circuit breaker", middlewares: []middleware.Middleware{&middleware.CircuitBreaker{}, &middleware.CircuitBreaker{}}},
		{name: "loop guard", middlewares: []middleware.Middleware{middleware.NewLoopGuard(), middleware.NewLoopGuard()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("unexpected", nil)}}}
			_, err := New(context.Background(), WithConfig(&Config{Model: chatModel, Middlewares: tc.middlewares}))
			if err == nil {
				t.Fatal("duplicate stateful middleware name was accepted")
			}
			if !strings.Contains(err.Error(), "duplicate stateful middleware name") {
				t.Fatalf("unexpected error: %v", err)
			}
			if chatModel.calls != 0 {
				t.Fatalf("model calls=%d", chatModel.calls)
			}
		})
	}
}

func TestDuplicateStatelessMiddlewareNamesRemainSupported(t *testing.T) {
	ctx := context.Background()
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	middlewares := []middleware.Middleware{
		&orderedMiddleware{name: "shared", order: new([]string)},
		&orderedMiddleware{name: "shared", order: new([]string)},
	}
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, Middlewares: middlewares}))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	result, err := graph.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("input")})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Content != "done" || chatModel.calls != 1 {
		t.Fatalf("result=%v model calls=%d", result, chatModel.calls)
	}
}

func TestRun_TranscriptObservesCanonicalEvents(t *testing.T) {
	var writers []*transcriptWriter
	template := &middleware.Transcript{Open: func(_ context.Context, threadID, runID string) (io.WriteCloser, error) {
		if threadID != "thread" || runID == "" {
			t.Fatal("missing run identity")
		}
		w := &transcriptWriter{}
		writers = append(writers, w)
		return w, nil
	}}
	for range 2 {
		var delivered []types.RuntimeEvent
		chatModel := &sequenceModel{responses: [][]*schema.Message{
			{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
			{schema.AssistantMessage("done", nil)},
		}}
		graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, ThreadID: "thread", ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}}}, Middlewares: []middleware.Middleware{template}, Emit: func(_ context.Context, e types.RuntimeEvent) error { delivered = append(delivered, e); return nil }}))
		if err != nil {
			t.Fatal(err)
		}
		_, executeErr := graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("go")})
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		if len(delivered) == 0 || delivered[len(delivered)-1].Kind != "turn_end" {
			t.Fatal("missing final event")
		}
		for i := range delivered {
			if delivered[i].Sequence != uint64(i+1) {
				t.Fatal("event sequence diverged")
			}
		}
		w := writers[len(writers)-1]
		if w.closes != 1 {
			t.Fatalf("close count=%d", w.closes)
		}
		decoder := json.NewDecoder(bytes.NewReader(w.Bytes()))
		var roles []string
		for {
			var record struct {
				Role    string              `json:"role"`
				Message *messagepkg.Message `json:"message"`
			}
			err := decoder.Decode(&record)
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if record.Message == nil {
				t.Fatal("missing original message")
			}
			roles = append(roles, record.Role)
		}
		if len(roles) != 4 || roles[0] != "user" || roles[1] != "assistant" || roles[2] != "tool" || roles[3] != "assistant" {
			t.Fatalf("duplicated or missing transcript messages: %v", roles)
		}
	}
	if len(writers) != 2 || writers[0] == writers[1] {
		t.Fatal("shared run writer")
	}
}

func TestRun_TranscriptWriteFailureClosesWriterAndStopsModel(t *testing.T) {
	want := errors.New("transcript disk failure")
	w := &transcriptWriter{fail: want}
	chatModel := &sequenceModel{}
	graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, Middlewares: []middleware.Middleware{&middleware.Transcript{Open: func(context.Context, string, string) (io.WriteCloser, error) { return w, nil }}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("go")})
	if !errors.Is(err, want) || chatModel.calls != 0 || w.closes != 1 {
		t.Fatalf("err=%v model=%d closes=%d", err, chatModel.calls, w.closes)
	}
}

func TestRun_PatchDanglingToolCallsOnlyInModelRequest(t *testing.T) {
	ctx := context.Background()
	history := conversation.New("thread", nil, nil, nil, 0, nil)
	assistant := messagepkg.NewAssistantMessage("", []schema.ToolCall{
		{ID: "done", Function: schema.FunctionCall{Name: "read_file", Arguments: "{}"}},
		{ID: "interrupted", Function: schema.FunctionCall{Name: "write_file", Arguments: "{}"}},
	})
	addHistoryErr := history.AddHistory(ctx, "old", messagepkg.NewUserMessage("old input"), assistant, messagepkg.NewToolMessage("already done", "done"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("new answer", nil)}}}
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, Conversation: history}))
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := graph.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("continue")})
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	repairs := 0
	for i, message := range chatModel.inputs[0] {
		if message.Role == schema.Tool && message.ToolCallID == "interrupted" {
			repairs++
			if message.Content == "" || i+1 >= len(chatModel.inputs[0]) || chatModel.inputs[0][i+1].Role != schema.User {
				t.Fatalf("invalid repair position: %+v", chatModel.inputs[0])
			}
		}
	}
	if repairs != 1 {
		t.Fatalf("repairs=%d", repairs)
	}
	for _, message := range history.GetHistory(ctx) {
		if message.Role == schema.Tool && message.ToolCallID == "interrupted" {
			t.Fatal("synthetic result persisted as real execution")
		}
	}
	if len(assistant.ToolCalls) != 2 {
		t.Fatal("original assistant changed")
	}
}

func TestRun_PlanRestoresFromCheckpointAfterContextCompaction(t *testing.T) {
	ctx := context.Background()
	chatModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "plan-call", Type: "function", Function: schema.FunctionCall{Name: "update_plan", Arguments: `{"todos":[{"content":"inspect repository","status":"in_progress"}]}`}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	published := 0
	config := Config{Model: chatModel, RunID: "plan-run", CheckpointStore: &checkpointMemory{}, Middlewares: []middleware.Middleware{middleware.NewPlan()}, ToolDescriptors: []tools.ToolDescriptor{tools.NewUpdatePlanTool(func(context.Context, tools.PlanUpdate) error { published++; return nil })}}
	first, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	config.Emit = nil
	first.config.Emit = func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "tool_end" {
			first.Interrupt()
		}
		return nil
	}
	_, err = first.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("inspect")}, WithCheckpointID("plan-checkpoint"))
	_, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatalf("expected checkpoint: %v", err)
	}
	if published != 1 || len(first.runState.Plan) != 1 {
		t.Fatalf("published=%d state=%+v", published, first.runState.Plan)
	}
	// Model context no longer contains the tool exchange, as after compaction.
	config.Conversation = conversation.New("thread", nil, nil, nil, 0, nil)
	addHistoryErr := config.Conversation.AddHistory(ctx, "plan-run", messagepkg.NewUserMessage("compacted summary"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	restored, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Invoke(ctx, nil, WithCheckpointID("plan-checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "done" || published != 1 || chatModel.calls != 2 {
		t.Fatalf("output=%v published=%d model=%d", out, published, chatModel.calls)
	}
	if len(restored.runState.Plan) != 1 || restored.runState.Plan[0].Step != "inspect repository" {
		t.Fatalf("restored plan=%+v", restored.runState.Plan)
	}
	reminders, instructions := 0, 0
	for _, message := range chatModel.inputs[1] {
		if strings.Contains(message.Content, "[in_progress] inspect repository") {
			reminders++
		}
		if strings.Contains(message.Content, "<plan_mode>") && strings.Contains(message.Content, "update_plan") {
			instructions++
		}
	}
	if reminders != 1 || instructions != 1 {
		t.Fatalf("reminders=%d instructions=%d", reminders, instructions)
	}
	for _, message := range restored.conversation.GetHistory(ctx) {
		if strings.Contains(message.Content, "<plan_mode>") {
			t.Fatal("planning instruction polluted durable history")
		}
	}
}

func TestRun_PlanEventsUseGraphSequenceAndDeliveryErrorsAreFatal(t *testing.T) {
	for _, failDelivery := range []bool{false, true} {
		chatModel := &sequenceModel{responses: [][]*schema.Message{
			{schema.AssistantMessage("", []schema.ToolCall{{ID: "plan", Function: schema.FunctionCall{Name: "update_plan", Arguments: `{"plan":"inspect"}`}}})},
			{schema.AssistantMessage("done", nil)},
		}}
		var events []types.RuntimeEvent
		want := errors.New("event transport failed")
		graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, Middlewares: []middleware.Middleware{middleware.NewPlan()}, ToolDescriptors: []tools.ToolDescriptor{tools.NewUpdatePlanTool(nil)}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
			events = append(events, event)
			if failDelivery && event.Kind == "plan_updated" {
				return want
			}
			return nil
		}}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("go")})
		if failDelivery {
			if !errors.Is(err, want) || chatModel.calls != 1 {
				t.Fatalf("delivery error swallowed: err=%v calls=%d", err, chatModel.calls)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		planIndex, startIndex, endIndex, count := -1, -1, -1, 0
		for i, event := range events {
			if event.Sequence != uint64(i+1) {
				t.Fatalf("sequence=%d position=%d", event.Sequence, i)
			}
			switch event.Kind {
			case "tool_start":
				startIndex = i
			case "plan_updated":
				planIndex = i
				count++
			case "tool_end":
				endIndex = i
			}
		}
		if count != 1 || startIndex < 0 || planIndex <= startIndex || (!failDelivery && endIndex <= planIndex) {
			t.Fatalf("start=%d plan=%d end=%d count=%d", startIndex, planIndex, endIndex, count)
		}
	}
}

// A prompt middleware must not grant tools that were not independently configured.
func TestRun_PromptMiddlewareDoesNotRegisterTools(t *testing.T) {
	for _, test := range []struct {
		name       string
		middleware middleware.Middleware
		prompt     string
	}{
		{"plan", middleware.NewPlan(), "<plan_mode>"},
		{"skill", middleware.NewSkillMiddleware(emptySkillLoader{}), "Available project skills"},
	} {
		t.Run(test.name, func(t *testing.T) {
			chatModel := &publicModel{}
			graph, err := New(context.Background(), WithModel(chatModel), WithMiddleware(test.middleware))
			if err != nil {
				t.Fatal(err)
			}
			defer graph.Close(context.Background())
			_, err = graph.Invoke(context.Background(), []*messagepkg.Message{messagepkg.NewUserMessage("hello")})
			if err != nil {
				t.Fatal(err)
			}
			if len(chatModel.infos) != 0 {
				t.Fatalf("prompt middleware implicitly registered tools: %+v", chatModel.infos)
			}
			found := false
			for _, message := range chatModel.inputs[0] {
				found = found || strings.Contains(message.Content, test.prompt)
			}
			if !found {
				t.Fatal("middleware prompt was lost")
			}
		})
	}
}

type emptySkillLoader struct{}

func (emptySkillLoader) ListSkills(context.Context) ([]*skills.SkillMetadata, error) {
	return nil, nil
}
