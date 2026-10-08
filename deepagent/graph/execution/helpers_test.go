package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/conversation"
	filesystempkg "eino-cli/deepagent/graph/filesystem"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type terminalWriteFailureStore struct {
	checkpointMemory
	failure error
}

func (terminalWriteFailureStore *terminalWriteFailureStore) Set(ctx context.Context, id string, raw []byte) error {
	var envelope checkpointer.Envelope
	err := json.Unmarshal(raw, &envelope)
	if err != nil {
		return err
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	decodeErr := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
	if decodeErr != nil {
		return decodeErr
	}
	if snapshot.MapValues["State"].JSONValue.Phase == types.PhaseCompleted && terminalWriteFailureStore.failure != nil {
		return terminalWriteFailureStore.failure
	}
	return terminalWriteFailureStore.checkpointMemory.Set(ctx, id, raw)
}

type checkpointLifecycle struct {
	middleware.BaseMiddleware
	before, after *int
}

func (checkpointLifecycle *checkpointLifecycle) PrepareRun(context.Context, *types.RunState) error {
	*checkpointLifecycle.before++
	return nil
}

func (checkpointLifecycle *checkpointLifecycle) FinishRun(context.Context, *types.RunState, error) error {
	*checkpointLifecycle.after++
	return nil
}

type compactionEventConversation struct {
	*conversation.Conversation
	calls int
	err   error
}

func (*compactionEventConversation) NeedsCompaction(context.Context) bool { return true }

func (compactionEventConversation *compactionEventConversation) Compact(context.Context, string) (*types.ContextTokenUsage, error) {
	compactionEventConversation.calls++
	if compactionEventConversation.err != nil {
		return nil, compactionEventConversation.err
	}
	usage := compactionEventConversation.GetContextUsage()
	return &usage, nil
}

type paritySummaryModel struct{ sequenceModel }

func (*paritySummaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("retained summary", nil), nil
}

type fakeToolCounter struct{ total int }

func (fakeToolCounter *fakeToolCounter) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "counter", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"delta": {Type: schema.Integer, Required: true},
	})}, nil
}

func (fakeToolCounter *fakeToolCounter) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	fakeToolCounter.total++
	return "ok", nil
}

type explicitReadOnlyTool struct{ fakeToolCounter }

func (*explicitReadOnlyTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "readonly_counter"}, nil
}

type eventStreamTool struct{}

func (*eventStreamTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stream_tool"}, nil
}

func (*eventStreamTool) StreamableRun(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
	return schema.StreamReaderFromArray([]string{"one", "two"}), nil
}

type inputEventConversation struct {
	*conversation.Conversation
	failure error
}

func (inputEventConversation *inputEventConversation) AddHistory(ctx context.Context, run string, messages ...*messagepkg.Message) error {
	if inputEventConversation.failure != nil && messages[0].Content == "second" {
		return inputEventConversation.failure
	}
	return inputEventConversation.Conversation.AddHistory(ctx, run, messages...)
}

type sequenceModel struct {
	calls     int
	inputs    [][]*schema.Message
	responses [][]*schema.Message
}

func (sequenceModel *sequenceModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return sequenceModel, nil
}

func (sequenceModel *sequenceModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected non-stream model call")
}

func (sequenceModel *sequenceModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sequenceModel.inputs = append(sequenceModel.inputs, append([]*schema.Message(nil), input...))
	index := sequenceModel.calls
	sequenceModel.calls++
	if index >= len(sequenceModel.responses) {
		return nil, fmt.Errorf("unexpected model call %d", index)
	}
	return schema.StreamReaderFromArray(sequenceModel.responses[index]), nil
}

type checkpointMemory struct {
	values map[string][]byte
	fail   bool
}

func (checkpointMemory *checkpointMemory) Get(_ context.Context, id string) ([]byte, bool, error) {
	value, ok := checkpointMemory.values[id]
	return value, ok, nil
}

func (checkpointMemory *checkpointMemory) Set(_ context.Context, id string, value []byte) error {
	if checkpointMemory.fail {
		return fmt.Errorf("checkpoint save failed")
	}
	if checkpointMemory.values == nil {
		checkpointMemory.values = map[string][]byte{}
	}
	checkpointMemory.values[id] = append([]byte(nil), value...)
	return nil
}

type namedCountingTool struct {
	name  string
	count int
}

func (namedCountingTool *namedCountingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: namedCountingTool.name}, nil
}

func (namedCountingTool *namedCountingTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	namedCountingTool.count++
	return namedCountingTool.name, nil
}

type eagerModel struct {
	toolStarted <-chan struct{}
	calls       int
}

func (eagerModel *eagerModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return eagerModel, nil
}

func (eagerModel *eagerModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}

func (eagerModel *eagerModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	eagerModel.calls++
	if eagerModel.calls == 2 {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		index := 9
		writer.Send(schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Index: &index, Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}}), nil)
		select {
		case <-eagerModel.toolStarted:
		case <-ctx.Done():
			writer.Send(nil, ctx.Err())
		}
	}()
	return reader, nil
}

type cancelModel struct {
	started chan struct{}
	stopped chan struct{}
}

func (cancelModel *cancelModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return cancelModel, nil
}

func (cancelModel *cancelModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}

func (cancelModel *cancelModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	close(cancelModel.started)
	<-ctx.Done()
	close(cancelModel.stopped)
	return nil, ctx.Err()
}

type legacyParityMemoryCheckpoints struct {
	mu   sync.Mutex
	data map[string][]byte
}

var _ compose.CheckPointStore = (*legacyParityMemoryCheckpoints)(nil)

func (legacyParityMemoryCheckpoints *legacyParityMemoryCheckpoints) Get(_ context.Context, id string) ([]byte, bool, error) {
	legacyParityMemoryCheckpoints.mu.Lock()
	defer legacyParityMemoryCheckpoints.mu.Unlock()
	value, ok := legacyParityMemoryCheckpoints.data[id]
	return append([]byte(nil), value...), ok, nil
}

func (legacyParityMemoryCheckpoints *legacyParityMemoryCheckpoints) Set(_ context.Context, id string, value []byte) error {
	legacyParityMemoryCheckpoints.mu.Lock()
	defer legacyParityMemoryCheckpoints.mu.Unlock()
	if legacyParityMemoryCheckpoints.data == nil {
		legacyParityMemoryCheckpoints.data = make(map[string][]byte)
	}
	legacyParityMemoryCheckpoints.data[id] = append([]byte(nil), value...)
	return nil
}

func newTestLocalFilesystem(t *testing.T, config *filesystempkg.LocalFilesystemConfig) *filesystempkg.LocalFilesystem {
	t.Helper()
	filesystem, err := filesystempkg.NewLocalFilesystem(config, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close(context.Background()) })
	return filesystem
}

type orderedMiddleware struct {
	middleware.BaseMiddleware
	name  string
	order *[]string
}

func (orderedMiddleware *orderedMiddleware) GetName() string { return orderedMiddleware.name }

func (orderedMiddleware *orderedMiddleware) PrepareRun(context.Context, *types.RunState) error {
	*orderedMiddleware.order = append(*orderedMiddleware.order, "before:"+orderedMiddleware.name)
	return nil
}

func (orderedMiddleware *orderedMiddleware) FinishRun(context.Context, *types.RunState, error) error {
	*orderedMiddleware.order = append(*orderedMiddleware.order, "after:"+orderedMiddleware.name)
	return nil
}

func (orderedMiddleware *orderedMiddleware) WrapModel(next middleware.ModelHandler) middleware.ModelHandler {
	return func(ctx context.Context, input []*messagepkg.Message) (*schema.StreamReader[*messagepkg.Message], error) {
		*orderedMiddleware.order = append(*orderedMiddleware.order, "model:"+orderedMiddleware.name)
		return next(ctx, input)
	}
}

type endOrderMiddleware struct {
	middleware.BaseMiddleware
	after bool
}

func (*endOrderMiddleware) PrepareRun(context.Context, *types.RunState) error { return nil }

func (endOrderMiddleware *endOrderMiddleware) FinishRun(context.Context, *types.RunState, error) error {
	endOrderMiddleware.after = true
	return nil
}

type modelTransformMiddleware struct {
	middleware.BaseMiddleware
	before, after int
	failure       error
}

func (modelTransformMiddleware *modelTransformMiddleware) ModifyModelRequest(_ context.Context, _ []*messagepkg.Message, messages []*messagepkg.Message, _ *types.GraphState) ([]*messagepkg.Message, error) {
	modelTransformMiddleware.before++
	if modelTransformMiddleware.failure != nil {
		return nil, modelTransformMiddleware.failure
	}
	return append([]*messagepkg.Message{messagepkg.NewSystemMessage("middleware prompt")}, messages...), nil
}

func (modelTransformMiddleware *modelTransformMiddleware) ModifyModelStreamResponse(_ context.Context, stream *schema.StreamReader[*messagepkg.Message], _ *types.GraphState) (*schema.StreamReader[*messagepkg.Message], error) {
	modelTransformMiddleware.after++
	return schema.StreamReaderWithConvert(stream, func(message *messagepkg.Message) (*messagepkg.Message, error) {
		copy := *message
		copy.Content = "rewritten"
		return &copy, nil
	}), nil
}

type transcriptWriter struct {
	bytes.Buffer
	closes int
	fail   error
}

func (transcriptWriter *transcriptWriter) Write(p []byte) (int, error) {
	if transcriptWriter.closes != 0 {
		return 0, errors.New("write after close")
	}
	if transcriptWriter.fail != nil {
		return 0, transcriptWriter.fail
	}
	return transcriptWriter.Buffer.Write(p)
}

func (transcriptWriter *transcriptWriter) Close() error { transcriptWriter.closes++; return nil }

type pendingWriteFailure struct {
	checkpointMemory
	writes  int
	failure error
	initial []byte
}

func (pendingWriteFailure *pendingWriteFailure) Set(ctx context.Context, id string, raw []byte) error {
	var envelope checkpointer.Envelope
	err := json.Unmarshal(raw, &envelope)
	if err != nil {
		return err
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	decodeErr := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
	if decodeErr != nil {
		return decodeErr
	}
	if len(snapshot.MapValues["State"].JSONValue.Pending) == 0 {
		return pendingWriteFailure.checkpointMemory.Set(ctx, id, raw)
	}
	pendingWriteFailure.writes++
	if pendingWriteFailure.writes == 1 {
		pendingWriteFailure.initial = append([]byte(nil), raw...)
	}
	if pendingWriteFailure.writes == 2 {
		return pendingWriteFailure.failure
	}
	return pendingWriteFailure.checkpointMemory.Set(ctx, id, raw)
}

// Capture both Eino's original write and the later interrupt-ID enrichment.
// The original write must already be safe if enrichment fails or the process exits.
type pendingSnapshotStore struct {
	checkpointMemory
	writes [][]byte
}

func (pendingSnapshotStore *pendingSnapshotStore) Set(ctx context.Context, id string, raw []byte) error {
	pendingSnapshotStore.writes = append(pendingSnapshotStore.writes, append([]byte(nil), raw...))
	return pendingSnapshotStore.checkpointMemory.Set(ctx, id, raw)
}

func buildTestConfig(opts ...Option) *Config {
	config := &Config{}
	for _, opt := range opts {
		opt(config)
	}
	return config
}

type publicModel struct {
	infos  []*schema.ToolInfo
	inputs [][]*schema.Message
	call   bool
}

func (publicModel *publicModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	publicModel.infos = infos
	return publicModel, nil
}

func (publicModel *publicModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (publicModel *publicModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	publicModel.inputs = append(publicModel.inputs, input)
	message := schema.AssistantMessage("done", nil)
	if publicModel.call && len(publicModel.inputs) == 1 {
		message = schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: `{"delta":3}`}}})
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type promptContractMiddleware struct {
	middleware.BaseMiddleware
	seen bool
}

func (promptContractMiddleware *promptContractMiddleware) GetName() string { return "prompt_contract" }

func (promptContractMiddleware *promptContractMiddleware) BuildPrompt(context.Context) ([]*messagepkg.Message, error) {
	return []*messagepkg.Message{messagepkg.NewSystemMessage("instructions")}, nil
}

func (promptContractMiddleware *promptContractMiddleware) ModifyModelRequest(_ context.Context, initial, messages []*messagepkg.Message, _ *types.GraphState) ([]*messagepkg.Message, error) {
	if len(initial) != 1 || initial[0].Role != schema.System {
		return nil, errors.New("initialContext no longer contains middleware prompts")
	}
	promptContractMiddleware.seen = true
	return messages, nil
}

type overlappingRunModel struct {
	ready   chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (overlappingRunModel *overlappingRunModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return overlappingRunModel, nil
}

func (*overlappingRunModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected non-stream model call")
}

func (overlappingRunModel *overlappingRunModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	overlappingRunModel.calls.Add(1)
	for _, message := range input {
		if message.Role == schema.Tool {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
		}
	}
	select {
	case overlappingRunModel.ready <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-overlappingRunModel.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	call := schema.ToolCall{ID: "same-call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{call})}), nil
}

type blockingResourceMiddleware struct {
	middleware.BaseMiddleware
	entered chan struct{}
	release chan struct{}
}

func (blockingResourceMiddleware *blockingResourceMiddleware) Close(context.Context) error {
	close(blockingResourceMiddleware.entered)
	<-blockingResourceMiddleware.release
	return nil
}

type resourceMiddleware struct {
	middleware.BaseMiddleware
	name                string
	order               *[]string
	beforeErr, closeErr error
	closed              int
}

func (resourceMiddleware *resourceMiddleware) GetName() string { return resourceMiddleware.name }

func (resourceMiddleware *resourceMiddleware) PrepareRun(context.Context, *types.RunState) error {
	return resourceMiddleware.beforeErr
}

func (resourceMiddleware *resourceMiddleware) FinishRun(context.Context, *types.RunState, error) error {
	return nil
}

func (resourceMiddleware *resourceMiddleware) Close(ctx context.Context) error {
	resourceMiddleware.closed++
	if resourceMiddleware.order != nil {
		*resourceMiddleware.order = append(*resourceMiddleware.order, resourceMiddleware.name)
	}
	if ctx.Err() != nil {
		return errors.New("cleanup received canceled context")
	}
	return resourceMiddleware.closeErr
}

// Checkpoint reads must not hold the Agent mutex: Close needs it to cancel.
type cancelableCheckpointRead struct {
	entered chan struct{}
}

func (cancelableCheckpointRead *cancelableCheckpointRead) Get(ctx context.Context, _ string) ([]byte, bool, error) {
	close(cancelableCheckpointRead.entered)
	<-ctx.Done()
	return nil, false, ctx.Err()
}

func (*cancelableCheckpointRead) Set(context.Context, string, []byte) error {
	return nil
}

type streamErrorMiddleware struct {
	middleware.BaseMiddleware
	reader *schema.StreamReader[*messagepkg.Message]
	err    error
}

func (*streamErrorMiddleware) GetName() string { return "stream_open_error" }

func (streamErrorMiddleware *streamErrorMiddleware) WrapModel(middleware.ModelHandler) middleware.ModelHandler {
	return func(context.Context, []*messagepkg.Message) (*schema.StreamReader[*messagepkg.Message], error) {
		return streamErrorMiddleware.reader, streamErrorMiddleware.err
	}
}

type streamErrorTool struct{ reader *schema.StreamReader[string] }

func (*streamErrorTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stream_error"}, nil
}

func (streamErrorTool *streamErrorTool) StreamableRun(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
	return streamErrorTool.reader, errors.New("tool opening failed")
}

func testChildApprovalResume(t *testing.T, allow bool) {
	ctx := context.Background()
	first, approved := &namedCountingTool{name: "first"}, &namedCountingTool{name: "approved"}
	chatModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "child-task", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"perform child work"}`}}})},
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "first", Function: schema.FunctionCall{Name: "first", Arguments: "{}"}}, {ID: "approved", Function: schema.FunctionCall{Name: "approved", Arguments: "{}"}}})},
		{schema.AssistantMessage("child done", nil)},
		{schema.AssistantMessage("parent done", nil)},
	}}
	store := &checkpointMemory{}
	graphConfig := Config{SubAgents: []*SubAgent{{Name: "general-purpose"}}, Model: chatModel, ThreadID: "parent", RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: first}, {Tool: approved, RequiresApproval: true}}}
	graph, err := New(ctx, WithConfig(&graphConfig))
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(ctx, []*messagepkg.Message{messagepkg.NewUserMessage("delegate")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		t.Fatalf("missing child approval: %v %+v", err, info)
	}
	if first.count != 1 || approved.count != 0 || chatModel.calls != 2 {
		t.Fatalf("before resume counts=%d/%d model=%d", first.count, approved.count, chatModel.calls)
	}
	if len(store.values) != 1 {
		t.Fatalf("child created external sidecar: %d entries", len(store.values))
	}
	if len(graph.runState.Extensions["child_checkpoint/child-task"]) == 0 {
		t.Fatal("child checkpoint not embedded")
	}
	graphConfig.Conversation = graph.conversation
	restored, err := New(ctx, WithConfig(&graphConfig))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved", Approved: allow}}))
	if err != nil {
		t.Fatal(err)
	}
	wantApproved := 0
	if allow {
		wantApproved = 1
	}
	if out.Content != "parent done" || chatModel.calls != 4 || first.count != 1 || approved.count != wantApproved {
		t.Fatalf("out=%v model=%d counts=%d/%d phase=%s", out, chatModel.calls, first.count, approved.count, restored.runState.Phase)
	}
	if len(restored.runState.Extensions["child_checkpoint/child-task"]) != 0 {
		t.Fatal("completed child retained stale checkpoint")
	}
	childInput := chatModel.inputs[2]
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

func (parallelChildModel *parallelChildModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return parallelChildModel, nil
}

func (*parallelChildModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("must stream")
}

func (parallelChildModel *parallelChildModel) Stream(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
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
	parallelChildModel.mu.Lock()
	parallelChildModel.started++
	if parallelChildModel.started == 2 {
		close(parallelChildModel.both)
	}
	parallelChildModel.mu.Unlock()
	select {
	case <-parallelChildModel.both:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "approval-" + name, Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}), nil
}

type childUsageModel struct {
	sequenceModel
	check func()
}

func (childUsageModel *childUsageModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return childUsageModel, nil
}

func (childUsageModel *childUsageModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if childUsageModel.calls == 1 {
		childUsageModel.check()
	}
	return childUsageModel.sequenceModel.Stream(ctx, input, opts...)
}

type childModel struct{ inputs [][]*schema.Message }

func (childModel *childModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return childModel, nil
}

func (childModel *childModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}

func (childModel *childModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	childModel.inputs = append(childModel.inputs, append([]*schema.Message(nil), input...))
	if input[len(input)-1].Role == schema.Tool {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("child done", nil)}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}), nil
}

type boundedChildRunner struct {
	mu           sync.Mutex
	active, peak int
	started      chan string
	release      chan struct{}
}

func (boundedChildRunner *boundedChildRunner) Run(ctx context.Context, req tools.ChildRequest, _ types.ModelChunkSink) (*messagepkg.Message, error) {
	boundedChildRunner.mu.Lock()
	boundedChildRunner.active++
	if boundedChildRunner.active > boundedChildRunner.peak {
		boundedChildRunner.peak = boundedChildRunner.active
	}
	boundedChildRunner.mu.Unlock()
	defer func() { boundedChildRunner.mu.Lock(); boundedChildRunner.active--; boundedChildRunner.mu.Unlock() }()
	boundedChildRunner.started <- req.Prompt
	select {
	case <-boundedChildRunner.release:
		return messagepkg.NewAssistantMessage(req.Prompt, nil), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func writeSubAgentSpec(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := os.WriteFile(filepath.Join(dir, "SUBAGENT.yaml"), []byte(content), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
}

type countingTool struct {
	count   atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type panicTool struct{ countingTool }

func (*panicTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	panic("tool crashed")
}

func (*countingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "counter"}, nil
}

func (countingTool *countingTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	countingTool.count.Add(1)
	if countingTool.started != nil {
		countingTool.once.Do(func() { close(countingTool.started) })
	}
	if countingTool.release != nil {
		select {
		case <-countingTool.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return args, nil
}

type identityTool struct{ countingTool }

func (*identityTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	return GetToolCallID(ctx), nil
}

type failingContractTool struct {
	countingTool
	failure error
}

func (failingContractTool *failingContractTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", failingContractTool.failure
}

type countingStreamTool struct{ countingTool }

func (countingStreamTool *countingStreamTool) StreamableRun(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
	countingStreamTool.count.Add(1)
	return schema.StreamReaderFromArray([]string{"done"}), nil
}

type executionFenceStore struct {
	checkpointMemory
	fenced  []byte
	failure error
}

func (executionFenceStore *executionFenceStore) Set(ctx context.Context, id string, raw []byte) error {
	var envelope checkpointer.Envelope
	err := json.Unmarshal(raw, &envelope)
	if err != nil {
		return err
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	decodeErr := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
	if decodeErr != nil {
		return decodeErr
	}
	for _, call := range snapshot.MapValues["State"].JSONValue.Calls {
		if call.Status == types.CallOutcomeUnknown {
			executionFenceStore.fenced = append([]byte(nil), raw...)
			if executionFenceStore.failure != nil {
				return executionFenceStore.failure
			}
		}
	}
	return executionFenceStore.checkpointMemory.Set(ctx, id, raw)
}

type imageTool struct{ calls int }

func (*imageTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "image"}, nil
}

func (imageTool *imageTool) InvokableRun(context.Context, *schema.ToolArgument, ...tool.Option) (*schema.ToolResult, error) {
	imageTool.calls++
	url := "https://example.test/image.png"
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{
		{Type: schema.ToolPartTypeText, Text: "image description"},
		{Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}}},
	}}, nil
}

type imageStreamTool struct {
	calls     int
	arguments string
	run       func(context.Context) (*schema.StreamReader[*schema.ToolResult], error)
}

func (*imageStreamTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "image_stream"}, nil
}

func (imageStreamTool *imageStreamTool) StreamableRun(ctx context.Context, args *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	imageStreamTool.calls++
	imageStreamTool.arguments = args.Text
	if imageStreamTool.run != nil {
		return imageStreamTool.run(ctx)
	}
	url := "https://example.test/stream.png"
	return schema.StreamReaderFromArray([]*schema.ToolResult{
		{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "first "}}},
		{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "second"}, {Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}}}}},
	}), nil
}

func newEnhancedStreamModel() *sequenceModel {
	return &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image_stream", Arguments: "{}"}}})}}}
}

func newUsageReply(text string, calls ...schema.ToolCall) *schema.Message {
	message := schema.AssistantMessage(text, calls)
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
	return message
}

type webMaskTestTool struct{ name string }

type graphWebMaskContextKey struct{}

func (webMaskTestTool *webMaskTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: webMaskTestTool.name}, nil
}

func (*webMaskTestTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", nil
}

type failingInfoTool struct {
	fakeToolCounter
	err error
}

func (failingInfoTool *failingInfoTool) Info(context.Context) (*schema.ToolInfo, error) {
	return nil, failingInfoTool.err
}
