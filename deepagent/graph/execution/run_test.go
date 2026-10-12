package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/graph/conversation"
	filesystempkg "eino-cli/deepagent/graph/filesystem"
	"eino-cli/deepagent/graph/middleware"
	deeptools "eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

func TestSelectBackendProvidesCommandExecution(t *testing.T) {
	ctx := context.Background()
	chatModel := &publicModel{}
	filesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(ctx)
	graph, err := New(ctx, Config{Model: chatModel, FilesystemConfig: &deeptools.FilesystemConfig{}, Filesystem: filesystem})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	found := false
	for _, info := range chatModel.infos {
		if info.Name == "execute" {
			found = true
		}
	}
	if !found {
		t.Fatal("local filesystem does not expose command execution")
	}
}

func TestFilesystemFeatureRequiresConfigAndFilesystem(t *testing.T) {
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	for _, test := range []struct {
		name       string
		config     *deeptools.FilesystemConfig
		filesystem agentmodel.ToolFilesystem
		read       bool
		write      bool
		command    bool
	}{
		{name: "no config", filesystem: filesystem},
		{name: "no filesystem", config: &deeptools.FilesystemConfig{}},
		{name: "defaults", config: &deeptools.FilesystemConfig{}, filesystem: filesystem, read: true, write: true, command: true},
		{name: "read only", config: &deeptools.FilesystemConfig{ReadOnly: true}, filesystem: filesystem, read: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			chatModel := &publicModel{}
			graph, err := New(context.Background(), Config{Model: chatModel, FilesystemConfig: test.config, Filesystem: test.filesystem})
			if err != nil {
				t.Fatal(err)
			}
			defer graph.Close(context.Background())
			names := make(map[string]bool)
			for _, info := range chatModel.infos {
				names[info.Name] = true
			}
			if names["read_file"] != test.read || names["write_file"] != test.write || names["execute"] != test.command {
				t.Fatalf("unexpected model tools: %v", names)
			}
		})
	}
}

func TestNew_RequiresModel(t *testing.T) {
	_, err := New(context.Background(), Config{})
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("expected model required error, got %v", err)
	}
}

func TestCollectAllTools_ReadOnlyBoundaryRejectsUnknownCapabilities(t *testing.T) {
	ctx := context.Background()
	config := &Config{ReadOnlyToolsOnly: true}
	chatModel := &publicModel{}
	config.Model = chatModel
	graph, err := New(ctx, Config{Model: chatModel, ReadOnlyToolsOnly: true, ToolDescriptors: []agentmodel.ToolDescriptor{{Tool: &fakeToolCounter{}}, {Tool: &explicitReadOnlyTool{}, ReadOnly: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	if len(chatModel.infos) != 1 || chatModel.infos[0].Name != "readonly_counter" {
		t.Fatalf("read-only tools = %v", chatModel.infos)
	}
}

func TestNewCopiesConfiguration(t *testing.T) {
	source := Config{Model: &publicModel{}, FilesystemConfig: &deeptools.FilesystemConfig{ReadOnly: true}, Prompts: []*agentmodel.Message{agentmodel.NewSystemMessage("original")}}
	graph, err := New(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	graph.config.Prompts[0] = agentmodel.NewSystemMessage("changed")
	graph.config.FilesystemConfig.ReadOnly = false
	if source.Prompts[0].Content != "original" || !source.FilesystemConfig.ReadOnly {
		t.Fatal("Graph mutated caller configuration")
	}
}

func TestToolMaskExcludesToolFromModel(t *testing.T) {
	chatModel := &publicModel{}
	graph, err := New(context.Background(), Config{
		Model:           chatModel,
		ToolDescriptors: []agentmodel.ToolDescriptor{{Tool: &fakeToolCounter{}}},
		ToolMask:        func(_ context.Context, info *schema.ToolInfo) bool { return info.Name != "counter" },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	if len(chatModel.infos) != 0 {
		t.Fatalf("masked tool exposed: %v", chatModel.infos)
	}
}

func TestToolPolicyDeniesWithoutRunningTool(t *testing.T) {
	counter := &fakeToolCounter{}
	chatModel := &publicModel{call: true}
	graph, err := New(context.Background(), Config{
		Model:           chatModel,
		ToolDescriptors: []agentmodel.ToolDescriptor{{Tool: counter}},
		Policy: agentmodel.PolicyFunc(func(context.Context, agentmodel.ToolCall, agentmodel.ToolDescriptor) (agentmodel.Decision, error) {
			return agentmodel.Decision{Action: agentmodel.Deny, Reason: "blocked"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	_, err = graph.Invoke(context.Background(), []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("go")}}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if counter.total != 0 || len(chatModel.inputs) != 2 {
		t.Fatalf("executions=%d models=%d", counter.total, len(chatModel.inputs))
	}
	last := chatModel.inputs[1][len(chatModel.inputs[1])-1]
	if last.Role != schema.Tool || last.Content != "blocked" {
		t.Fatalf("denial missing: %+v", last)
	}
}

func TestPublicEntryUsesCanonicalAgentAndContext(t *testing.T) {
	ctx := context.Background()
	chatModel := &publicModel{}
	var agent *Graph
	seen := false
	handler := (&callbacks.HandlerBuilder{}).OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		seen = true
		if GetGraph(ctx) != agent || GetWholeGraphState(ctx) != agent.GetGraphState() {
			t.Error("public context lookup lost canonical agent")
		}
		return ctx
	}).Build()
	var err error
	agent, err = New(ctx, Config{Model: chatModel, Callbacks: []callbacks.Handler{

		handler}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(ctx)
	var canonical *Graph = agent
	if canonical != agent {
		t.Fatal("extra agent wrapper")
	}
	_, err = agent.Invoke(ctx, []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("go")}}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("callback not called")
	}
}

func TestPublicConversationDoesNotDuplicateHistory(t *testing.T) {
	ctx := context.Background()
	chatModel := &publicModel{call: true}
	currentMiddleware := &promptContractMiddleware{}
	history := conversation.New("", nil, nil, nil, 0, nil)
	graph, err := New(ctx, Config{Model: chatModel, Conversation: history, Middlewares: []agentmodel.Middleware{currentMiddleware}, ToolDescriptors: []agentmodel.ToolDescriptor{{Tool: &fakeToolCounter{}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	_, err = graph.Invoke(ctx, []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("go")}}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.GetHistory(ctx)) != 4 {
		t.Fatalf("conversation history = %v", history.GetHistory(ctx))
	}
	if !currentMiddleware.seen || len(chatModel.inputs) != 2 || len(chatModel.inputs[0]) != 2 || len(chatModel.inputs[1]) != 4 {
		t.Fatalf("history duplicated: %v", chatModel.inputs)
	}
	if chatModel.inputs[1][0].Role != schema.System || chatModel.inputs[1][1].Content != "go" || chatModel.inputs[1][2].Role != schema.Assistant || chatModel.inputs[1][3].Role != schema.Tool {
		t.Fatal("history order changed")
	}
}

func TestRun_ConcurrentRunsDoNotShareState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	chatModel := &overlappingRunModel{ready: make(chan struct{}, 2), release: make(chan struct{})}
	tool := &countingTool{}
	config := &Config{Model: chatModel, Middlewares: []agentmodel.Middleware{guard}, ToolDescriptors: []agentmodel.ToolDescriptor{{Tool: tool}}}
	agents := make([]*Graph, 2)
	for i := range agents {
		var err error
		agents[i], err = New(ctx, *config)
		if err != nil {
			t.Fatal(err)
		}
		defer agents[i].Close(context.Background())
	}
	if agents[0].middlewares[0] == agents[1].middlewares[0] {
		t.Fatal("runs share mutable LoopGuard")
	}
	results := make(chan error, 2)
	for _, agent := range agents {
		go func(graph *Graph) {
			out, err := graph.Invoke(ctx, []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("go")}}, RunOptions{})
			if err == nil && (out == nil || out.Content != "done") {
				err = fmt.Errorf("unexpected output: %v", out)
			}
			results <- err
		}(agent)
	}
	for range agents {
		select {
		case <-chatModel.ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(chatModel.release)
	for range agents {
		err := <-results
		if err != nil {
			t.Fatal(err)
		}
	}
	got := tool.count.Load()
	if got != 2 {
		t.Fatalf("shared middleware suppressed a tool call: %d", got)
	}
}

func TestRun_SecondRunRejectedWithoutSideEffects(t *testing.T) {
	ctx := context.Background()
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	currentMiddleware := &resourceMiddleware{name: "resource"}
	graph, err := New(ctx, Config{Model: chatModel, Middlewares: []agentmodel.Middleware{currentMiddleware}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(ctx, []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("first")}}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state := graph.runState
	history := graph.conversation.GetHistory(ctx)
	out, err := graph.Invoke(ctx, []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("second")}}, RunOptions{})
	if err == nil || out != nil || chatModel.calls != 1 || currentMiddleware.closed != 1 {
		t.Fatalf("out=%v err=%v model=%d closed=%d", out, err, chatModel.calls, currentMiddleware.closed)
	}
	if graph.runState != state || !reflect.DeepEqual(history, graph.conversation.GetHistory(ctx)) {
		t.Fatal("rejected run changed state or history")
	}
}

func TestRun_NewAgentsShareCallerOwnedFilesystem(t *testing.T) {
	chatModel := &sequenceModel{}
	for range 2 {
		chatModel.responses = append(chatModel.responses,
			[]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "command", Function: schema.FunctionCall{Name: "execute", Arguments: `{"command":"pwd"}`}}})},
			[]*schema.Message{schema.AssistantMessage("done", nil)})
	}
	root := t.TempDir()
	filesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: root, VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(context.Background())
	for i := 0; i < 2; i++ {
		graph, err := New(context.Background(), Config{Model: chatModel, Filesystem: filesystem, FilesystemConfig: &deeptools.FilesystemConfig{DisableApplyPatch: true}, Policy: agentmodel.PolicyFunc(func(context.Context, agentmodel.ToolCall, agentmodel.ToolDescriptor) (agentmodel.Decision, error) {
			return agentmodel.Decision{Action: agentmodel.Allow}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		_, err = graph.Invoke(context.Background(), []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("where")}}, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		history := graph.conversation.GetHistory(context.Background())
		result := history[len(history)-2]
		if result.Role != schema.Tool || !strings.Contains(result.Content, "exit_code=0") || !strings.Contains(result.Content, root) {
			t.Fatalf("run=%d result=%+v", i, result)
		}
		err = graph.Close(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestClose_ConcurrentCallWaitsForUnstartedResourceCleanup(t *testing.T) {
	currentMiddleware := &blockingResourceMiddleware{entered: make(chan struct{}), release: make(chan struct{})}
	graph, err := New(context.Background(), Config{Model: &sequenceModel{}, Middlewares: []agentmodel.Middleware{currentMiddleware}})
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- graph.Close(context.Background()) }()
	defer func() {
		close(currentMiddleware.release)
		err := <-first
		if err != nil {
			t.Error(err)
		}
	}()
	<-currentMiddleware.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	closeErr := graph.Close(ctx)
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close returned before resource cleanup completed: %v", closeErr)
	}
}

func TestNew_ConstructionFailureClosesResourcesInReverseOrder(t *testing.T) {
	want, closeErr := errors.New("tool construction failed"), errors.New("resource close failed")
	var order []string
	first := &resourceMiddleware{name: "first", order: &order, closeErr: closeErr}
	second := &resourceMiddleware{name: "second", order: &order}
	graph, err := New(context.Background(), Config{Model: &sequenceModel{}, Middlewares: []agentmodel.Middleware{first, second}, ToolDescriptors: []agentmodel.ToolDescriptor{{Tool: &failingInfoTool{err: want}}}})
	if graph != nil || !errors.Is(err, want) || !errors.Is(err, closeErr) {
		t.Fatalf("agent=%v err=%v", graph, err)
	}
	if !reflect.DeepEqual(order, []string{"second", "first"}) || first.closed != 1 || second.closed != 1 {
		t.Fatalf("order=%v", order)
	}
}

func TestClose_UnstartedAgentClosesConstructedResourcesOnce(t *testing.T) {
	currentMiddleware := &resourceMiddleware{name: "resource"}
	graph, err := New(context.Background(), Config{Model: &sequenceModel{}, Middlewares: []agentmodel.Middleware{currentMiddleware}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		err := graph.Close(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	if currentMiddleware.closed != 1 {
		t.Fatalf("closed=%d", currentMiddleware.closed)
	}
}

func TestRun_FailedStartClosesResourcesAndPreservesBothErrors(t *testing.T) {
	want, closeErr := errors.New("before run failed"), errors.New("close failed")
	currentMiddleware := &resourceMiddleware{name: "resource", beforeErr: want, closeErr: closeErr}
	graph, err := New(context.Background(), Config{Model: &sequenceModel{}, Middlewares: []agentmodel.Middleware{currentMiddleware}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(context.Background(), []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("go")}}, RunOptions{})
	if !errors.Is(err, want) || !errors.Is(err, closeErr) || currentMiddleware.closed != 1 {
		t.Fatalf("err=%v closed=%d", err, currentMiddleware.closed)
	}
	_, err = graph.Invoke(context.Background(), nil, RunOptions{})
	if err == nil || currentMiddleware.closed != 1 {
		t.Fatalf("failed run allowed reuse: err=%v closed=%d", err, currentMiddleware.closed)
	}
	err = graph.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if currentMiddleware.closed != 1 {
		t.Fatalf("double close: %d", currentMiddleware.closed)
	}
}

func TestRun_CloseCancelsCheckpointRead(t *testing.T) {
	ctx := context.Background()
	store := &cancelableCheckpointRead{entered: make(chan struct{})}
	chatModel := &sequenceModel{}
	graph, err := New(ctx, Config{Model: chatModel, CheckpointStore: store})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	finished := make(chan error, 1)
	go func() {
		_, runErr := graph.Invoke(runCtx, nil, RunOptions{CheckpointID: "saved"})
		finished <- runErr
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("checkpoint read did not start")
	}
	_, err = graph.Invoke(ctx, nil, RunOptions{})
	if err == nil {
		t.Fatal("second run entered during checkpoint read")
	}
	closed := make(chan error, 1)
	go func() { closed <- graph.Close(ctx) }()
	select {
	case err = <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind checkpoint read")
	}
	err = <-finished
	if !errors.Is(err, context.Canceled) || chatModel.calls != 0 {
		t.Fatalf("err=%v model calls=%d", err, chatModel.calls)
	}
	graph.mu.Lock()
	active := graph.invoking
	graph.mu.Unlock()
	if active {
		t.Fatal("failed startup retained the execution claim")
	}
}

func TestRun_ModelOpenErrorClosesReturnedStream(t *testing.T) {
	reader, writer := schema.Pipe[*agentmodel.Message](0)
	defer writer.Close()
	want := errors.New("model opening failed")
	graph, err := New(context.Background(), Config{Model: &sequenceModel{}, Middlewares: []agentmodel.Middleware{&streamErrorMiddleware{reader: reader, err: want}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(context.Background(), []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("go")}}, RunOptions{})
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	closed := make(chan bool, 1)
	go func() { closed <- writer.Send(nil, nil) }()
	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("stream remains open")
		}
	case <-time.After(time.Second):
		reader.Close()
		t.Fatal("stream was not closed")
	}
}

func TestToolExecutor_OpenErrorClosesReturnedStream(t *testing.T) {
	reader, writer := schema.Pipe[string](0)
	defer writer.Close()
	toolSet, err := deeptools.NewToolSet(context.Background(), []agentmodel.ToolDescriptor{{Tool: &streamErrorTool{reader: reader}}})
	if err != nil {
		t.Fatal(err)
	}
	toolExecutor := newToolExecutor(toolSet, 1, nil)
	result, err := toolExecutor.executeToolCall(context.Background(), agentmodel.ToolCall{ID: "call", Name: "stream_error", Arguments: "{}"}, nil)
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%v err=%v", result, err)
	}
	closed := make(chan bool, 1)
	go func() { closed <- writer.Send("", nil) }()
	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("stream remains open")
		}
	case <-time.After(time.Second):
		reader.Close()
		t.Fatal("stream was not closed")
	}
}

// 同一条输入的 ID、消息和 metadata 一起传递，Graph 只修改自己的消息副本。
func TestInvokePreservesCompleteInputs(t *testing.T) {
	ctx := context.Background()
	identified := agentmodel.NewUserMessage("identified")
	identified.MessageID, identified.ThreadID, identified.RunID = "source", "old-thread", "old-run"
	anonymous := agentmodel.NewUserMessage("anonymous")
	anonymous.MessageID = "not-the-delivery-id"
	inputs := []agentmodel.RunInput{
		{MessageID: "skip", Meta: "skip"},
		{MessageID: "delivery", Message: identified, Meta: map[string]string{"sender": "user"}},
		{Message: anonymous, Meta: "second-meta"},
	}
	var consumed []agentmodel.RunInput
	graph, err := New(ctx, Config{Model: &publicModel{}, ThreadID: "thread", RunID: "run", Emit: func(_ context.Context, event agentmodel.RuntimeEvent) error {
		if event.Kind == "input_consumed" {
			consumed = append(consumed, event.Data.(agentmodel.RunInput))
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	_, err = graph.Invoke(ctx, inputs, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(consumed) != 2 || consumed[0].MessageID != "delivery" || consumed[1].MessageID != "" {
		t.Fatalf("input identity changed: %+v", consumed)
	}
	if consumed[0].Message.MessageID != "delivery" || consumed[1].Message.MessageID != "" || consumed[0].Message.ThreadID != "thread" || consumed[0].Message.RunID != "run" {
		t.Fatalf("graph message identity changed: %+v", consumed)
	}
	if !reflect.DeepEqual(consumed[0].Meta, inputs[1].Meta) || consumed[1].Meta != "second-meta" {
		t.Fatal("input metadata lost or shifted past nil message")
	}
	if identified.MessageID != "source" || identified.ThreadID != "old-thread" || identified.RunID != "old-run" || anonymous.MessageID != "not-the-delivery-id" {
		t.Fatal("Graph mutated caller messages")
	}
}

func TestInvokeKeepsDefaultAndInvocationCallbacks(t *testing.T) {
	ctx := context.Background()
	var defaultCalls, invocationCalls int
	defaultHandler := (&callbacks.HandlerBuilder{}).OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		defaultCalls++
		return ctx
	}).Build()
	invocationHandler := (&callbacks.HandlerBuilder{}).OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		invocationCalls++
		return ctx
	}).Build()
	graph, err := New(ctx, Config{Model: &publicModel{}, Callbacks: []callbacks.Handler{defaultHandler}})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	_, err = graph.Invoke(ctx, []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("go")}}, RunOptions{Callbacks: []callbacks.Handler{invocationHandler}})
	if err != nil {
		t.Fatal(err)
	}
	if defaultCalls == 0 || invocationCalls != defaultCalls {
		t.Fatalf("default=%d invocation=%d", defaultCalls, invocationCalls)
	}
}
