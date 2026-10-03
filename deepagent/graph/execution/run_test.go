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
	"eino-cli/deepagent/graph/tools"
	deeptools "eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

func TestSelectBackendProvidesCommandExecution(t *testing.T) {
	ctx := context.Background()
	m := &publicModel{}
	filesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(ctx)
	a, err := New(ctx, WithModel(m), WithFilesystem(filesystem))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	found := false
	for _, info := range m.infos {
		if info.Name == "execute" {
			found = true
		}
	}
	if !found {
		t.Fatal("local filesystem does not expose command execution")
	}
}

func TestFeatureConfigPresenceControlsEnablement(t *testing.T) {
	configured := buildCreateConfig()
	if configured.FilesystemConfig != nil || configured.WebConfig != nil {
		t.Fatalf("zero config unexpectedly enables features: %+v", configured)
	}

	configured = buildCreateConfig(WithFilesystemConfig(nil), WithWeb())
	if configured.FilesystemConfig == nil {
		t.Fatal("WithFilesystemConfig() did not create filesystem config")
	}
	if configured.WebConfig == nil || !configured.WebConfig.EnableWebSearch || !configured.WebConfig.EnableFetchURL {
		t.Fatalf("WithWeb() config = %+v", configured.WebConfig)
	}
}

func TestNew_RequiresModel(t *testing.T) {
	_, err := New(context.Background())
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("expected model required error, got %v", err)
	}
}

func TestCollectAllTools_ReadOnlyBoundaryRejectsUnknownCapabilities(t *testing.T) {
	ctx := context.Background()
	config := &Config{ReadOnlyToolsOnly: true}
	m := &publicModel{}
	config.Model = m
	a, err := New(ctx, WithConfig(config), WithTools(
		deeptools.ToolDescriptor{Tool: &fakeToolCounter{}},
		deeptools.ToolDescriptor{Tool: &explicitReadOnlyTool{}, ReadOnly: true},
	))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	if len(m.infos) != 1 || m.infos[0].Name != "readonly_counter" {
		t.Fatalf("read-only tools = %v", m.infos)
	}
}

func TestWithConfigCopiesInput(t *testing.T) {
	source := &Config{FilesystemConfig: &FilesystemConfig{ReadOnly: true}, Prompts: []*schema.Message{schema.SystemMessage("original")}}
	configured := buildCreateConfig(WithConfig(source))
	configured.Prompts[0] = schema.SystemMessage("changed")
	configured.FilesystemConfig.ReadOnly = false
	if source.Prompts[0].Content != "original" || !source.FilesystemConfig.ReadOnly {
		t.Fatal("WithConfig mutated the source")
	}
}

func TestToolMaskExcludesToolFromModel(t *testing.T) {
	m := &publicModel{}
	a, err := New(context.Background(), WithConfig(&Config{
		Model:           m,
		ToolDescriptors: []deeptools.ToolDescriptor{{Tool: &fakeToolCounter{}}},
		ToolMask:        func(_ context.Context, info *schema.ToolInfo) bool { return info.Name != "counter" },
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	if len(m.infos) != 0 {
		t.Fatalf("masked tool exposed: %v", m.infos)
	}
}

func TestToolPolicyDeniesWithoutRunningTool(t *testing.T) {
	counter := &fakeToolCounter{}
	m := &publicModel{call: true}
	a, err := New(context.Background(), WithConfig(&Config{
		Model:           m,
		ToolDescriptors: []deeptools.ToolDescriptor{{Tool: counter}},
		Policy: deeptools.PolicyFunc(func(context.Context, types.ToolCall, deeptools.ToolDescriptor) (deeptools.Decision, error) {
			return deeptools.Decision{Action: deeptools.Deny, Reason: "blocked"}, nil
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	_, err = a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if counter.total != 0 || len(m.inputs) != 2 {
		t.Fatalf("executions=%d models=%d", counter.total, len(m.inputs))
	}
	last := m.inputs[1][len(m.inputs[1])-1]
	if last.Role != schema.Tool || last.Content != "blocked" {
		t.Fatalf("denial missing: %+v", last)
	}
}

func TestPublicEntryUsesCanonicalAgentAndContext(t *testing.T) {
	ctx := context.Background()
	m := &publicModel{}
	var agent *Graph
	seen := false
	handler := (&callbacks.HandlerBuilder{}).OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		seen = true
		if GetGraph(ctx) != agent || GetWholeGraphState(ctx) != agent.GraphState() {
			t.Error("public context lookup lost canonical agent")
		}
		return ctx
	}).Build()
	var err error
	agent, err = New(ctx, WithModel(m), WithDefaultCallbacks(handler))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(ctx)
	var canonical *Graph = agent
	if canonical != agent {
		t.Fatal("extra agent wrapper")
	}
	_, err = agent.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("callback not called")
	}
}

func TestPublicResumeOptionsMergeWithoutMutatingCaller(t *testing.T) {
	data := map[string]any{"first": 1}
	var opts RunOptions
	WithResumeData(data)(&opts)
	WithResumeData(map[string]any{"second": 2})(&opts)
	if len(data) != 1 || len(opts.ResumeData) != 2 {
		t.Fatal("resume options lost prior answers or mutated caller")
	}
}

func TestPublicConversationDoesNotDuplicateHistory(t *testing.T) {
	ctx := context.Background()
	m := &publicModel{call: true}
	mw := &promptContractMiddleware{}
	history := conversation.New("", nil, nil, nil)
	a, err := New(ctx, WithConfig(&Config{Model: m, Conversation: history}), WithMiddleware(mw), WithTools(tools.ToolDescriptor{Tool: &fakeToolCounter{}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.History(ctx)) != 4 {
		t.Fatalf("conversation history = %v", history.History(ctx))
	}
	if !mw.seen || len(m.inputs) != 2 || len(m.inputs[0]) != 2 || len(m.inputs[1]) != 4 {
		t.Fatalf("history duplicated: %v", m.inputs)
	}
	if m.inputs[1][0].Role != schema.System || m.inputs[1][1].Content != "go" || m.inputs[1][2].Role != schema.Assistant || m.inputs[1][3].Role != schema.Tool {
		t.Fatal("history order changed")
	}
}

func TestRun_ConcurrentRunsDoNotShareState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	m := &overlappingRunModel{ready: make(chan struct{}, 2), release: make(chan struct{})}
	tool := &countingTool{}
	cfg := &Config{Model: m, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []deeptools.ToolDescriptor{{Tool: tool}}}
	agents := make([]*Graph, 2)
	for i := range agents {
		var err error
		agents[i], err = New(ctx, WithConfig(cfg))
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
		go func(a *Graph) {
			out, err := a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
			if err == nil && (out == nil || out.Content != "done") {
				err = fmt.Errorf("unexpected output: %v", out)
			}
			results <- err
		}(agent)
	}
	for range agents {
		select {
		case <-m.ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(m.release)
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
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	mw := &resourceMiddleware{name: "resource"}
	a, err := New(ctx, WithConfig(&Config{Model: m, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("first")})
	if err != nil {
		t.Fatal(err)
	}
	state := a.state
	history := a.conversation.History(ctx)
	optionsCalled := false
	out, err := a.Invoke(ctx, []*schema.Message{schema.UserMessage("second")}, func(*RunOptions) { optionsCalled = true })
	if err == nil || out != nil || optionsCalled || m.calls != 1 || mw.closed != 1 {
		t.Fatalf("out=%v err=%v options=%v model=%d closed=%d", out, err, optionsCalled, m.calls, mw.closed)
	}
	if a.state != state || !reflect.DeepEqual(history, a.conversation.History(ctx)) {
		t.Fatal("rejected run changed state or history")
	}
}

func TestRun_NewAgentsShareCallerOwnedFilesystem(t *testing.T) {
	m := &sequenceModel{}
	for range 2 {
		m.responses = append(m.responses,
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
		a, err := New(context.Background(), WithConfig(&Config{Model: m, Filesystem: filesystem, FilesystemConfig: &FilesystemConfig{DisableApplyPatch: true}, Policy: deeptools.PolicyFunc(func(context.Context, types.ToolCall, deeptools.ToolDescriptor) (deeptools.Decision, error) {
			return deeptools.Decision{Action: deeptools.Allow}, nil
		})}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("where")})
		if err != nil {
			t.Fatal(err)
		}
		history := a.conversation.History(context.Background())
		result := history[len(history)-2]
		if result.Role != schema.Tool || !strings.Contains(result.Content, "exit_code=0") || !strings.Contains(result.Content, root) {
			t.Fatalf("run=%d result=%+v", i, result)
		}
		err = a.Close(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestClose_ConcurrentCallWaitsForUnstartedResourceCleanup(t *testing.T) {
	mw := &blockingResourceMiddleware{entered: make(chan struct{}), release: make(chan struct{})}
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- a.Close(context.Background()) }()
	defer func() {
		close(mw.release)
		err := <-first
		if err != nil {
			t.Error(err)
		}
	}()
	<-mw.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	closeErr := a.Close(ctx)
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close returned before resource cleanup completed: %v", closeErr)
	}
}

func TestNew_ConstructionFailureClosesResourcesInReverseOrder(t *testing.T) {
	want, closeErr := errors.New("tool construction failed"), errors.New("resource close failed")
	var order []string
	first := &resourceMiddleware{name: "first", order: &order, closeErr: closeErr}
	second := &resourceMiddleware{name: "second", order: &order}
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{first, second}, ToolDescriptors: []tools.ToolDescriptor{{Tool: &failingInfoTool{err: want}}}}))
	if a != nil || !errors.Is(err, want) || !errors.Is(err, closeErr) {
		t.Fatalf("agent=%v err=%v", a, err)
	}
	if !reflect.DeepEqual(order, []string{"second", "first"}) || first.closed != 1 || second.closed != 1 {
		t.Fatalf("order=%v", order)
	}
}

func TestClose_UnstartedAgentClosesConstructedResourcesOnce(t *testing.T) {
	mw := &resourceMiddleware{name: "resource"}
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		err := a.Close(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	if mw.closed != 1 {
		t.Fatalf("closed=%d", mw.closed)
	}
}

func TestRun_FailedStartClosesResourcesAndPreservesBothErrors(t *testing.T) {
	want, closeErr := errors.New("before run failed"), errors.New("close failed")
	mw := &resourceMiddleware{name: "resource", beforeErr: want, closeErr: closeErr}
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || !errors.Is(err, closeErr) || mw.closed != 1 {
		t.Fatalf("err=%v closed=%d", err, mw.closed)
	}
	_, err = a.Invoke(context.Background(), nil)
	if err == nil || mw.closed != 1 {
		t.Fatalf("failed run allowed reuse: err=%v closed=%d", err, mw.closed)
	}
	err = a.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mw.closed != 1 {
		t.Fatalf("double close: %d", mw.closed)
	}
}

func TestRun_CloseCancelsCheckpointRead(t *testing.T) {
	ctx := context.Background()
	store := &cancelableCheckpointRead{entered: make(chan struct{})}
	m := &sequenceModel{}
	a, err := New(ctx, WithConfig(&Config{Model: m, CheckpointStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	finished := make(chan error, 1)
	go func() {
		_, runErr := a.Invoke(runCtx, nil, WithCheckpointID("saved"))
		finished <- runErr
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("checkpoint read did not start")
	}
	_, err = a.Invoke(ctx, nil)
	if err == nil {
		t.Fatal("second run entered during checkpoint read")
	}
	closed := make(chan error, 1)
	go func() { closed <- a.Close(ctx) }()
	select {
	case err = <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind checkpoint read")
	}
	err = <-finished
	if !errors.Is(err, context.Canceled) || m.calls != 0 {
		t.Fatalf("err=%v model calls=%d", err, m.calls)
	}
	a.mu.Lock()
	active := a.invoking
	a.mu.Unlock()
	if active {
		t.Fatal("failed startup retained the execution claim")
	}
}

func TestRun_ModelOpenErrorClosesReturnedStream(t *testing.T) {
	reader, writer := schema.Pipe[*schema.Message](0)
	defer writer.Close()
	want := errors.New("model opening failed")
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{&streamErrorMiddleware{reader: reader, err: want}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
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
	toolSet, err := deeptools.NewToolSet(context.Background(), []deeptools.ToolDescriptor{{Tool: &streamErrorTool{reader: reader}}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor(toolSet, 1, nil)
	result, err := e.execute(context.Background(), types.ToolCall{ID: "call", Name: "stream_error", Arguments: "{}"}, nil)
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
