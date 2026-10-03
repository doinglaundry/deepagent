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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type terminalWriteFailureStore struct {
	checkpointMemory
	failure error
}

func (s *terminalWriteFailureStore) Set(ctx context.Context, id string, raw []byte) error {
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
	if snapshot.MapValues["State"].JSONValue.Phase == types.PhaseCompleted && s.failure != nil {
		return s.failure
	}
	return s.checkpointMemory.Set(ctx, id, raw)
}

type checkpointLifecycle struct {
	middleware.BaseMiddleware
	before, after *int
}

func (m *checkpointLifecycle) BeforeRun(context.Context, *types.RunState) error {
	*m.before++
	return nil
}

func (m *checkpointLifecycle) AfterRun(context.Context, *types.RunState, error) error {
	*m.after++
	return nil
}

type compactionEventConversation struct {
	*conversation.Conversation
	calls int
	err   error
}

func (*compactionEventConversation) CompactNeeded(context.Context) bool { return true }

func (c *compactionEventConversation) Compact(context.Context, string) (*conversation.ContextCompactedPayload, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return &conversation.ContextCompactedPayload{StrategyID: "test"}, nil
}

type paritySummaryModel struct{ sequenceModel }

func (*paritySummaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("retained summary", nil), nil
}

type fakeToolCounter struct{ total int }

func (t *fakeToolCounter) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "counter", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"delta": {Type: schema.Integer, Required: true},
	})}, nil
}

func (t *fakeToolCounter) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	t.total++
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

func (c *inputEventConversation) AddHistory(ctx context.Context, run string, messages ...*schema.Message) error {
	if c.failure != nil && messages[0].Content == "second" {
		return c.failure
	}
	return c.Conversation.AddHistory(ctx, run, messages...)
}

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

type namedCountingTool struct {
	name  string
	count int
}

func (t *namedCountingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *namedCountingTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	t.count++
	return t.name, nil
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

type legacyParityMemoryCheckpoints struct {
	mu   sync.Mutex
	data map[string][]byte
}

var _ compose.CheckPointStore = (*legacyParityMemoryCheckpoints)(nil)

func (s *legacyParityMemoryCheckpoints) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	return append([]byte(nil), value...), ok, nil
}

func (s *legacyParityMemoryCheckpoints) Set(_ context.Context, id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[id] = append([]byte(nil), value...)
	return nil
}

func mustLocalFilesystem(t *testing.T, cfg *filesystempkg.LocalFilesystemConfig) *filesystempkg.LocalFilesystem {
	t.Helper()
	filesystem, err := filesystempkg.NewLocalFilesystem(cfg, t.Name())
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

func (m *orderedMiddleware) Name() string { return m.name }

func (m *orderedMiddleware) BeforeRun(context.Context, *types.RunState) error {
	*m.order = append(*m.order, "before:"+m.name)
	return nil
}

func (m *orderedMiddleware) AfterRun(context.Context, *types.RunState, error) error {
	*m.order = append(*m.order, "after:"+m.name)
	return nil
}

func (m *orderedMiddleware) WrapModel(next middleware.ModelHandler) middleware.ModelHandler {
	return func(ctx context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		*m.order = append(*m.order, "model:"+m.name)
		return next(ctx, input)
	}
}

type endOrderMiddleware struct {
	middleware.BaseMiddleware
	after bool
}

func (*endOrderMiddleware) BeforeRun(context.Context, *types.RunState) error { return nil }

func (m *endOrderMiddleware) AfterRun(context.Context, *types.RunState, error) error {
	m.after = true
	return nil
}

type modelTransformMiddleware struct {
	middleware.BaseMiddleware
	before, after int
	failure       error
}

func (m *modelTransformMiddleware) ModifyModelRequest(_ context.Context, _ []*schema.Message, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
	m.before++
	if m.failure != nil {
		return nil, m.failure
	}
	return append([]*schema.Message{schema.SystemMessage("middleware prompt")}, messages...), nil
}

func (m *modelTransformMiddleware) ModifyModelStreamResponse(_ context.Context, stream *schema.StreamReader[*schema.Message], _ *types.GraphState) (*schema.StreamReader[*schema.Message], error) {
	m.after++
	return schema.StreamReaderWithConvert(stream, func(message *schema.Message) (*schema.Message, error) {
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

func (w *transcriptWriter) Write(p []byte) (int, error) {
	if w.closes != 0 {
		return 0, errors.New("write after close")
	}
	if w.fail != nil {
		return 0, w.fail
	}
	return w.Buffer.Write(p)
}

func (w *transcriptWriter) Close() error { w.closes++; return nil }

type pendingWriteFailure struct {
	checkpointMemory
	writes  int
	failure error
	initial []byte
}

func (s *pendingWriteFailure) Set(ctx context.Context, id string, raw []byte) error {
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
		return s.checkpointMemory.Set(ctx, id, raw)
	}
	s.writes++
	if s.writes == 1 {
		s.initial = append([]byte(nil), raw...)
	}
	if s.writes == 2 {
		return s.failure
	}
	return s.checkpointMemory.Set(ctx, id, raw)
}

// Capture both Eino's original write and the later interrupt-ID enrichment.
// The original write must already be safe if enrichment fails or the process exits.
type pendingSnapshotStore struct {
	checkpointMemory
	writes [][]byte
}

func (s *pendingSnapshotStore) Set(ctx context.Context, id string, raw []byte) error {
	s.writes = append(s.writes, append([]byte(nil), raw...))
	return s.checkpointMemory.Set(ctx, id, raw)
}

func buildCreateConfig(opts ...Option) *Config {
	cfg := &Config{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

type publicModel struct {
	infos  []*schema.ToolInfo
	inputs [][]*schema.Message
	call   bool
}

func (m *publicModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.infos = infos
	return m, nil
}

func (m *publicModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (m *publicModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input)
	message := schema.AssistantMessage("done", nil)
	if m.call && len(m.inputs) == 1 {
		message = schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: `{"delta":3}`}}})
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type promptContractMiddleware struct {
	middleware.BaseMiddleware
	seen bool
}

func (m *promptContractMiddleware) Name() string { return "prompt_contract" }

func (m *promptContractMiddleware) BuildPrompt(context.Context) ([]*schema.Message, error) {
	return []*schema.Message{schema.SystemMessage("instructions")}, nil
}

func (m *promptContractMiddleware) ModifyModelRequest(_ context.Context, initial, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
	if len(initial) != 1 || initial[0].Role != schema.System {
		return nil, errors.New("initialContext no longer contains middleware prompts")
	}
	m.seen = true
	return messages, nil
}

type overlappingRunModel struct {
	ready   chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (m *overlappingRunModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (*overlappingRunModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected non-stream model call")
}

func (m *overlappingRunModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls.Add(1)
	for _, message := range input {
		if message.Role == schema.Tool {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
		}
	}
	select {
	case m.ready <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-m.release:
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

func (m *blockingResourceMiddleware) Close(context.Context) error {
	close(m.entered)
	<-m.release
	return nil
}

type resourceMiddleware struct {
	middleware.BaseMiddleware
	name                string
	order               *[]string
	beforeErr, closeErr error
	closed              int
}

func (m *resourceMiddleware) Name() string { return m.name }

func (m *resourceMiddleware) BeforeRun(context.Context, *types.RunState) error { return m.beforeErr }

func (m *resourceMiddleware) AfterRun(context.Context, *types.RunState, error) error { return nil }

func (m *resourceMiddleware) Close(ctx context.Context) error {
	m.closed++
	if m.order != nil {
		*m.order = append(*m.order, m.name)
	}
	if ctx.Err() != nil {
		return errors.New("cleanup received canceled context")
	}
	return m.closeErr
}

// Checkpoint reads must not hold the Agent mutex: Close needs it to cancel.
type cancelableCheckpointRead struct {
	entered chan struct{}
}

func (s *cancelableCheckpointRead) Get(ctx context.Context, _ string) ([]byte, bool, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, false, ctx.Err()
}

func (*cancelableCheckpointRead) Set(context.Context, string, []byte) error {
	return nil
}

type streamErrorMiddleware struct {
	middleware.BaseMiddleware
	reader *schema.StreamReader[*schema.Message]
	err    error
}

func (*streamErrorMiddleware) Name() string { return "stream_open_error" }

func (m *streamErrorMiddleware) WrapModel(middleware.ModelHandler) middleware.ModelHandler {
	return func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return m.reader, m.err
	}
}

type streamErrorTool struct{ reader *schema.StreamReader[string] }

func (*streamErrorTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stream_error"}, nil
}

func (s *streamErrorTool) StreamableRun(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
	return s.reader, errors.New("tool opening failed")
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
	cfg := Config{SubAgents: []*SubAgent{{Name: "general-purpose"}}, Model: m, ThreadID: "parent", RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: first}, {Tool: approved, RequiresApproval: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("delegate")}, WithCheckpointID("checkpoint"))
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
	out, err := restored.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved", Approved: allow}}))
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

type childUsageModel struct {
	sequenceModel
	check func()
}

func (m *childUsageModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *childUsageModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.calls == 1 {
		m.check()
	}
	return m.sequenceModel.Stream(ctx, input, opts...)
}

type childModel struct{ inputs [][]*schema.Message }

func (m *childModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return m, nil }

func (m *childModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected Generate")
}

func (m *childModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
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

func writeSpec(t *testing.T, root, name, content string) {
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

func (t *countingTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	t.count.Add(1)
	if t.started != nil {
		t.once.Do(func() { close(t.started) })
	}
	if t.release != nil {
		select {
		case <-t.release:
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

func (t *failingContractTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", t.failure
}

type countingStreamTool struct{ countingTool }

func (t *countingStreamTool) StreamableRun(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
	t.count.Add(1)
	return schema.StreamReaderFromArray([]string{"done"}), nil
}

type executionFenceStore struct {
	checkpointMemory
	fenced  []byte
	failure error
}

func (s *executionFenceStore) Set(ctx context.Context, id string, raw []byte) error {
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
			s.fenced = append([]byte(nil), raw...)
			if s.failure != nil {
				return s.failure
			}
		}
	}
	return s.checkpointMemory.Set(ctx, id, raw)
}

type imageTool struct{ calls int }

func (*imageTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "image"}, nil
}

func (t *imageTool) InvokableRun(context.Context, *schema.ToolArgument, ...tool.Option) (*schema.ToolResult, error) {
	t.calls++
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

func (t *imageStreamTool) StreamableRun(ctx context.Context, args *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	t.calls++
	t.arguments = args.Text
	if t.run != nil {
		return t.run(ctx)
	}
	url := "https://example.test/stream.png"
	return schema.StreamReaderFromArray([]*schema.ToolResult{
		{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "first "}}},
		{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "second"}, {Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}}}}},
	}), nil
}

func enhancedStreamModel() *sequenceModel {
	return &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image_stream", Arguments: "{}"}}})}}}
}

func usageReply(text string, calls ...schema.ToolCall) *schema.Message {
	m := schema.AssistantMessage(text, calls)
	m.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
	return m
}

type webMaskTestTool struct{ name string }

type graphWebMaskContextKey struct{}

func (t *webMaskTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (*webMaskTestTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", nil
}

type failingInfoTool struct {
	fakeToolCounter
	err error
}

func (t *failingInfoTool) Info(context.Context) (*schema.ToolInfo, error) {
	return nil, t.err
}
