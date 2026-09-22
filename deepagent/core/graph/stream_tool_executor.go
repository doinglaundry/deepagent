package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"

	"eino-cli/deepagent/core/internal/toolerrors"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type streamToolExecutorInterruptInfo struct {
	CompletedCallIDs   []string
	InterruptedCallIDs []string
}

type streamToolExecutorInterruptState struct {
	ToolCalls      []schema.ToolCall
	CompletedCalls map[string][]*schema.Message
}

func init() {
	schema.RegisterName[*streamToolExecutorInterruptInfo]("_deepagents_stream_tool_executor_interrupt_info")
	schema.RegisterName[*streamToolExecutorInterruptState]("_deepagents_stream_tool_executor_interrupt_state")
}

// CreateStreamingToolLambda 构建直接消费模型输出流的 tools 节点。
// 完整 ToolCall 会立即执行，结果按原槽位输出并在下次模型调用前收敛。
func CreateStreamingToolLambda(ctx context.Context, conf *compose.ToolsNodeConfig) (*compose.Lambda, error) {
	streamToolExecutor, err := NewStreamToolExecutor(ctx, conf, nil)
	if err != nil {
		return nil, err
	}

	return compose.TransformableLambda(streamToolExecutor.Execute), nil
}

type ToolCallHook struct {
	OnToolStart    func(tool *schema.ToolCall)
	OnToolComplete func(tool *schema.ToolCall, result string)
	OnError        func(tool *schema.ToolCall, err error)
}

type StreamToolExecutor struct {
	conf            *compose.ToolsNodeConfig
	hook            *ToolCallHook
	toolCallsByName map[string]*toolCall
}

func NewStreamToolExecutor(
	ctx context.Context,
	conf *compose.ToolsNodeConfig,
	hook *ToolCallHook,
) (*StreamToolExecutor, error) {
	if conf == nil {
		return nil, errors.New("tools node config is required")
	}

	toolCallsByName := make(map[string]*toolCall, len(conf.Tools))
	for _, baseTool := range conf.Tools {
		info, err := baseTool.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get tool info for stream tool executor: %w", err)
		}

		tc := &toolCall{
			name:     info.Name,
			typeName: reflect.TypeOf(baseTool).String(),
		}

		if invokableTool, ok := baseTool.(einotool.InvokableTool); ok {
			tc.stream = streamToolEndpointFromInvokable(toolInvokableEndpoint(
				invokableTool,
				info.Name,
				tc.typeName,
				conf.ToolCallMiddlewares,
			))
		}

		if enhancedInvokableTool, ok := baseTool.(einotool.EnhancedInvokableTool); ok {
			tc.enhancedStream = enhancedStreamToolEndpointFromInvokable(enhancedInvokableEndpoint(
				enhancedInvokableTool,
				info.Name,
				tc.typeName,
				conf.ToolCallMiddlewares,
			))
		}

		if streamableTool, ok := baseTool.(einotool.StreamableTool); ok {
			tc.stream = toolStreamableEndpoint(
				streamableTool,
				info.Name,
				tc.typeName,
				conf.ToolCallMiddlewares,
			)
		}

		if enhancedStreamableTool, ok := baseTool.(einotool.EnhancedStreamableTool); ok {
			tc.enhancedStream = enhancedStreamableEndpoint(
				enhancedStreamableTool,
				info.Name,
				tc.typeName,
				conf.ToolCallMiddlewares,
			)
		}

		if tc.stream == nil && tc.enhancedStream == nil {
			return nil, fmt.Errorf(
				"tool %s is not invokable, streamable, enhanced invokable or enhanced streamable",
				info.Name,
			)
		}

		toolCallsByName[info.Name] = tc
	}

	return &StreamToolExecutor{
		conf:            conf,
		hook:            hook,
		toolCallsByName: toolCallsByName,
	}, nil
}

func (s *StreamToolExecutor) Execute(
	ctx context.Context,
	inputStream *schema.StreamReader[*schema.Message],
) (*schema.StreamReader[[]*schema.Message], error) {
	wasInterrupted, hasState, interruptState := compose.GetInterruptState[*streamToolExecutorInterruptState](ctx)
	if wasInterrupted && hasState && interruptState != nil {
		return s.resume(ctx, interruptState)
	}

	toolTasks, err := s.collectAndLaunch(ctx, inputStream)
	if err != nil {
		return nil, err
	}

	var interruptErrors []error
	for _, task := range toolTasks {
		if task.err != nil {
			interruptErrors = append(interruptErrors, task.err)
		}
	}

	if len(interruptErrors) == 0 {
		return s.mergeTaskStreams(toolTasks), nil
	}

	return nil, buildStreamToolInterrupt(ctx, toolTasks, interruptErrors)
}

func buildStreamToolInterrupt(
	ctx context.Context,
	toolTasks []*streamToolTask,
	interruptErrors []error,
) error {
	interruptState := &streamToolExecutorInterruptState{
		ToolCalls:      make([]schema.ToolCall, 0, len(toolTasks)),
		CompletedCalls: make(map[string][]*schema.Message),
	}
	interruptInfo := &streamToolExecutorInterruptInfo{}

	for _, task := range toolTasks {
		interruptState.ToolCalls = append(interruptState.ToolCalls, task.toolCall)

		if task.err != nil {
			interruptInfo.InterruptedCallIDs = append(interruptInfo.InterruptedCallIDs, task.toolCall.ID)
			continue
		}

		messages, err := collectMessageArrays(task.stream)
		if err != nil {
			return fmt.Errorf("collect tool output for %s failed: %w", task.toolCall.ID, err)
		}
		interruptState.CompletedCalls[task.toolCall.ID] = messages
		interruptInfo.CompletedCallIDs = append(interruptInfo.CompletedCallIDs, task.toolCall.ID)
	}

	return compose.CompositeInterrupt(ctx, interruptInfo, interruptState, interruptErrors...)
}

func (s *StreamToolExecutor) resume(
	ctx context.Context,
	state *streamToolExecutorInterruptState,
) (*schema.StreamReader[[]*schema.Message], error) {
	tasks := make([]*streamToolTask, 0, len(state.ToolCalls))

	for _, tc := range state.ToolCalls {
		if completed, ok := state.CompletedCalls[tc.ID]; ok {
			chunks := make([][]*schema.Message, 0, len(completed))
			for _, msg := range completed {
				if msg == nil {
					continue
				}
				chunks = append(chunks, []*schema.Message{CopyMessage(msg)})
			}
			tasks = append(tasks, &streamToolTask{
				toolCall: tc,
				index:    toolCallIndex(tc, len(tasks)),
				stream:   schema.StreamReaderFromArray(chunks),
			})
			continue
		}

		task := s.launchTask(ctx, tc, toolCallIndex(tc, len(tasks)))
		tasks = append(tasks, task)
	}

	var interruptErrs []error
	for _, task := range tasks {
		if task.err != nil {
			interruptErrs = append(interruptErrs, task.err)
		}
	}

	if len(interruptErrs) > 0 {
		nextState := &streamToolExecutorInterruptState{
			ToolCalls:      cloneToolCalls(state.ToolCalls),
			CompletedCalls: make(map[string][]*schema.Message),
		}
		info := &streamToolExecutorInterruptInfo{}

		for _, tc := range state.ToolCalls {
			if completed, ok := state.CompletedCalls[tc.ID]; ok {
				nextState.CompletedCalls[tc.ID] = cloneMessages(completed)
				info.CompletedCallIDs = append(info.CompletedCallIDs, tc.ID)
			}
		}

		for _, task := range tasks {
			if task.err != nil {
				info.InterruptedCallIDs = append(info.InterruptedCallIDs, task.toolCall.ID)
				continue
			}
			if _, ok := nextState.CompletedCalls[task.toolCall.ID]; ok {
				continue
			}

			msgs, readErr := collectMessageArrays(task.stream)
			if readErr != nil {
				return nil, fmt.Errorf("collect resumed tool output for %s failed: %w", task.toolCall.ID, readErr)
			}
			nextState.CompletedCalls[task.toolCall.ID] = msgs
			info.CompletedCallIDs = append(info.CompletedCallIDs, task.toolCall.ID)
		}

		return nil, compose.CompositeInterrupt(ctx, info, nextState, interruptErrs...)
	}

	return s.mergeTaskStreams(tasks), nil
}

func (s *StreamToolExecutor) collectAndLaunch(
	ctx context.Context,
	inputStream *schema.StreamReader[*schema.Message],
) (tasks []*streamToolTask, err error) {
	tasks = make([]*streamToolTask, 0)
	collector := NewToolCallCollector()

	for {
		chunk, err := inputStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		completeCalls := collector.Collect(ctx, chunk)
		for _, tc := range completeCalls {
			index := toolCallIndex(tc, len(tasks))
			tasks = append(tasks, s.launchTask(ctx, tc, index))
		}
	}

	repairedCalls := collector.GetRepairedToolCalls(ctx)
	for _, repairedCall := range repairedCalls {
		index := toolCallIndex(repairedCall, len(tasks))
		tasks = append(tasks, s.launchTask(ctx, repairedCall, index))
	}

	return tasks, nil
}

func (s *StreamToolExecutor) launchTask(ctx context.Context, tc schema.ToolCall, index int) *streamToolTask {
	if s.hook != nil && s.hook.OnToolStart != nil {
		s.hook.OnToolStart(&tc)
	}

	stream, err := s.executeToolCall(ctx, tc)
	if err != nil {
		if s.hook != nil && s.hook.OnError != nil {
			s.hook.OnError(&tc, err)
		}
		slog.WarnContext(ctx, fmt.Sprintf("[StreamToolExecutor:launchTask] tool %s launch failed: %v", tc.Function.Name, err))
		return &streamToolTask{toolCall: tc, index: index, err: err}
	}

	return &streamToolTask{
		toolCall: tc,
		index:    index,
		stream:   stream,
	}
}

func (s *StreamToolExecutor) executeToolCall(ctx context.Context, tc schema.ToolCall) (*schema.StreamReader[[]*schema.Message], error) {
	input, toolRun, err := s.prepareToolInput(ctx, tc)
	if err != nil {
		return nil, err
	}

	taskCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      toolRun.name,
		Type:      toolRun.typeName,
		Component: components.ComponentOfTool,
	})
	taskCtx = compose.AppendAddressSegment(taskCtx, compose.AddressSegmentTool, tc.ID)
	taskCtx = context.WithValue(taskCtx, streamToolCallIDKey{}, tc.ID)

	if toolRun.enhancedStream != nil {
		output, err := toolRun.enhancedStream(taskCtx, input)
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderWithConvert(output.Result, func(tr *schema.ToolResult) ([]*schema.Message, error) {
			msg := schema.ToolMessage("", tc.ID, schema.WithToolName(toolRun.name))
			parts, err := tr.ToMessageInputParts()
			if err != nil {
				return nil, err
			}
			msg.UserInputMultiContent = parts
			return []*schema.Message{msg}, nil
		}), nil
	}

	output, err := toolRun.stream(taskCtx, input)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderWithConvert(output.Result, func(chunk string) ([]*schema.Message, error) {
		return []*schema.Message{schema.ToolMessage(chunk, tc.ID, schema.WithToolName(toolRun.name))}, nil
	}), nil
}

func (s *StreamToolExecutor) prepareToolInput(ctx context.Context, tc schema.ToolCall) (*compose.ToolInput, *toolCall, error) {
	toolRun, found := s.toolCallsByName[tc.Function.Name]
	if !found {
		if s.conf == nil || s.conf.UnknownToolsHandler == nil {
			return nil, nil, fmt.Errorf("tool %s not found in stream tool executor registry", tc.Function.Name)
		}
		toolRun = newUnknownToolRun(tc.Function.Name, s.conf.UnknownToolsHandler)
	}

	args := tc.Function.Arguments
	if s.conf != nil && s.conf.ToolArgumentsHandler != nil {
		var err error
		args, err = s.conf.ToolArgumentsHandler(ctx, tc.Function.Name, args)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to executed tool[name:%s arguments:%s] arguments handler: %w", tc.Function.Name, tc.Function.Arguments, err)
		}
	}

	return &compose.ToolInput{
		Name:      tc.Function.Name,
		Arguments: args,
		CallID:    tc.ID,
	}, toolRun, nil
}

func (s *StreamToolExecutor) mergeTaskStreams(tasks []*streamToolTask) *schema.StreamReader[[]*schema.Message] {
	if len(tasks) == 0 {
		slog.WarnContext(context.Background(), "[StreamToolExecutor::mergeTaskStreams] no task stream available; emit single empty chunk to avoid empty-reader concat failure")
		return schema.StreamReaderFromArray([][]*schema.Message{{}})
	}

	streams := make([]*schema.StreamReader[[]*schema.Message], 0, len(tasks))
	total := len(tasks)
	for _, task := range tasks {
		if task.stream == nil {
			continue
		}
		streams = append(streams, normalizeTaskStream(task.stream, total, task.index))
	}

	if len(streams) == 0 {
		slog.WarnContext(context.Background(), "[StreamToolExecutor::mergeTaskStreams] tasks exist but all streams are nil; emit single empty chunk to avoid empty-reader concat failure")
		return schema.StreamReaderFromArray([][]*schema.Message{{}})
	}

	return schema.MergeStreamReaders(streams)
}

type streamToolTask struct {
	toolCall schema.ToolCall
	index    int
	stream   *schema.StreamReader[[]*schema.Message]
	err      error
}

func normalizeTaskStream(stream *schema.StreamReader[[]*schema.Message], total int, index int) *schema.StreamReader[[]*schema.Message] {
	return schema.StreamReaderWithConvert(stream, func(chunk []*schema.Message) ([]*schema.Message, error) {
		ret := make([]*schema.Message, total)
		for _, msg := range chunk {
			if msg == nil {
				continue
			}
			ret[index] = CopyMessage(msg)
			break
		}
		return ret, nil
	})
}

func collectMessageArrays(stream *schema.StreamReader[[]*schema.Message]) ([]*schema.Message, error) {
	if stream == nil {
		return nil, nil
	}
	defer stream.Close()

	var out []*schema.Message
	for {
		messages, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		for _, msg := range messages {
			if msg == nil {
				continue
			}
			out = append(out, CopyMessage(msg))
		}
	}
}

func toolCallIndex(tc schema.ToolCall, fallback int) int {
	if tc.Index != nil {
		return *tc.Index
	}
	return fallback
}

func cloneToolCalls(src []schema.ToolCall) []schema.ToolCall {
	if len(src) == 0 {
		return nil
	}

	ret := make([]schema.ToolCall, len(src))
	copy(ret, src)
	return ret
}

func cloneMessages(src []*schema.Message) []*schema.Message {
	if len(src) == 0 {
		return nil
	}

	ret := make([]*schema.Message, 0, len(src))
	for _, msg := range src {
		ret = append(ret, CopyMessage(msg))
	}
	return ret
}

type toolCall struct {
	name           string
	typeName       string
	stream         compose.StreamableToolEndpoint
	enhancedStream compose.EnhancedStreamableToolEndpoint
}

func toolInvokableEndpoint(
	t einotool.InvokableTool,
	name string,
	typeName string,
	middlewares []compose.ToolMiddleware,
) compose.InvokableToolEndpoint {
	endpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		callCtx := getToolCallCtx(ctx, name, typeName, input)
		result, err := t.InvokableRun(callCtx, input.Arguments, input.CallOptions...)
		if err != nil {
			if toolerrors.ShouldReturnAsResult(err) {
				result := toolerrors.Result(err)
				_ = callbacks.OnEnd(callCtx, &einotool.CallbackOutput{
					Response: result,
					Extra:    map[string]any{"tool_call_id": input.CallID},
				})
				return &compose.ToolOutput{Result: result}, nil
			}
			_ = callbacks.OnError(callCtx, err)
			return nil, err
		}
		_ = callbacks.OnEnd(callCtx, &einotool.CallbackOutput{
			Response: result,
			Extra:    map[string]any{"tool_call_id": input.CallID},
		})
		return &compose.ToolOutput{Result: result}, nil
	}
	for i := len(middlewares) - 1; i >= 0; i-- {
		if middleware := middlewares[i].Invokable; middleware != nil {
			endpoint = middleware(endpoint)
		}
	}
	return endpoint
}

func toolStreamableEndpoint(
	st einotool.StreamableTool,
	name string,
	typeName string,
	middlewares []compose.ToolMiddleware,
) compose.StreamableToolEndpoint {
	endpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		callCtx := getToolCallCtx(ctx, name, typeName, input)
		result, err := st.StreamableRun(callCtx, input.Arguments, input.CallOptions...)
		if err != nil {
			if toolerrors.ShouldReturnAsResult(err) {
				result := schema.StreamReaderFromArray([]string{toolerrors.Result(err)})
				output, wrapErr := wrapObservedToolStream(callCtx, result, input.CallID)
				if wrapErr != nil {
					return nil, wrapErr
				}
				return &compose.StreamToolOutput{Result: output}, nil
			}
			_ = callbacks.OnError(callCtx, err)
			return nil, err
		}
		output, err := wrapObservedToolStream(callCtx, result, input.CallID)
		if err != nil {
			return nil, err
		}
		return &compose.StreamToolOutput{Result: output}, nil
	}
	for i := len(middlewares) - 1; i >= 0; i-- {
		if middleware := middlewares[i].Streamable; middleware != nil {
			endpoint = middleware(endpoint)
		}
	}
	return endpoint
}

func enhancedInvokableEndpoint(
	eit einotool.EnhancedInvokableTool,
	name string,
	typeName string,
	middlewares []compose.ToolMiddleware,
) compose.EnhancedInvokableToolEndpoint {
	endpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
		callCtx := getToolCallCtx(ctx, name, typeName, input)
		result, err := eit.InvokableRun(callCtx, &schema.ToolArgument{Text: input.Arguments}, input.CallOptions...)
		if err != nil {
			if toolerrors.ShouldReturnAsResult(err) {
				result := textToolResult(toolerrors.Result(err))
				_ = callbacks.OnEnd(callCtx, &einotool.CallbackOutput{
					ToolOutput: result,
					Extra:      map[string]any{"tool_call_id": input.CallID},
				})
				return &compose.EnhancedInvokableToolOutput{Result: result}, nil
			}
			_ = callbacks.OnError(callCtx, err)
			return nil, err
		}
		_ = callbacks.OnEnd(callCtx, &einotool.CallbackOutput{
			ToolOutput: result,
			Extra:      map[string]any{"tool_call_id": input.CallID},
		})
		return &compose.EnhancedInvokableToolOutput{Result: result}, nil
	}
	for i := len(middlewares) - 1; i >= 0; i-- {
		if middleware := middlewares[i].EnhancedInvokable; middleware != nil {
			endpoint = middleware(endpoint)
		}
	}
	return endpoint
}

func enhancedStreamableEndpoint(
	est einotool.EnhancedStreamableTool,
	name string,
	typeName string,
	middlewares []compose.ToolMiddleware,
) compose.EnhancedStreamableToolEndpoint {
	endpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
		callCtx := getToolCallCtx(ctx, name, typeName, input)
		result, err := est.StreamableRun(callCtx, &schema.ToolArgument{Text: input.Arguments}, input.CallOptions...)
		if err != nil {
			if toolerrors.ShouldReturnAsResult(err) {
				result := schema.StreamReaderFromArray([]*schema.ToolResult{textToolResult(toolerrors.Result(err))})
				output, wrapErr := wrapObservedEnhancedToolStream(callCtx, result, input.CallID)
				if wrapErr != nil {
					return nil, wrapErr
				}
				return &compose.EnhancedStreamableToolOutput{Result: output}, nil
			}
			_ = callbacks.OnError(callCtx, err)
			return nil, err
		}
		output, err := wrapObservedEnhancedToolStream(callCtx, result, input.CallID)
		if err != nil {
			return nil, err
		}
		return &compose.EnhancedStreamableToolOutput{Result: output}, nil
	}
	for i := len(middlewares) - 1; i >= 0; i-- {
		if middleware := middlewares[i].EnhancedStreamable; middleware != nil {
			endpoint = middleware(endpoint)
		}
	}
	return endpoint
}

func streamToolEndpointFromInvokable(endpoint compose.InvokableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		output, err := endpoint(ctx, input)
		if err != nil {
			return nil, err
		}
		return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{output.Result})}, nil
	}
}

func enhancedStreamToolEndpointFromInvokable(endpoint compose.EnhancedInvokableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
		output, err := endpoint(ctx, input)
		if err != nil {
			return nil, err
		}
		return &compose.EnhancedStreamableToolOutput{Result: schema.StreamReaderFromArray([]*schema.ToolResult{output.Result})}, nil
	}
}

func textToolResult(text string) *schema.ToolResult {
	return &schema.ToolResult{
		Parts: []schema.ToolOutputPart{{
			Type: schema.ToolPartTypeText,
			Text: text,
		}},
	}
}

func newUnknownToolRun(
	name string,
	handler func(ctx context.Context, name, input string) (string, error),
) *toolCall {
	endpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		result, err := handler(ctx, input.Name, input.Arguments)
		if err != nil {
			return nil, err
		}
		return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{result})}, nil
	}
	return &toolCall{
		name:     name,
		typeName: "UnknownTool",
		stream:   endpoint,
	}
}

// streamToolCallIDKey is the context key for tool call ID injection.
// This mirrors compose.toolCallInfoKey but is defined here because compose does not export a setter.
type streamToolCallIDKey struct{}

// GetToolCallID retrieves the tool call ID for the current streaming tool execution.
// It falls back to compose.GetToolCallID for compatibility with the standard ToolsNode path.
func GetToolCallID(ctx context.Context) string {
	if v, ok := ctx.Value(streamToolCallIDKey{}).(string); ok && v != "" {
		return v
	}
	return compose.GetToolCallID(ctx)
}

func getToolCallCtx(ctx context.Context, name, typeName string, input *compose.ToolInput) context.Context {
	callCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      name,
		Type:      typeName,
		Component: components.ComponentOfTool,
	})
	callCtx = context.WithValue(callCtx, streamToolCallIDKey{}, input.CallID)
	callCtx = callbacks.OnStart(callCtx, &einotool.CallbackInput{
		ArgumentsInJSON: input.Arguments,
		Extra:           map[string]any{"tool_call_id": input.CallID},
	})
	return callCtx
}

func wrapObservedToolStream(ctx context.Context, stream *schema.StreamReader[string], callID string) (*schema.StreamReader[string], error) {
	cbStream := schema.StreamReaderWithConvert(stream, func(chunk string) (*einotool.CallbackOutput, error) {
		return &einotool.CallbackOutput{
			Response: chunk,
			Extra:    map[string]any{"tool_call_id": callID},
		}, nil
	})
	_, observed := callbacks.OnEndWithStreamOutput(ctx, cbStream)
	return schema.StreamReaderWithConvert(observed, func(chunk *einotool.CallbackOutput) (string, error) {
		if chunk == nil {
			return "", nil
		}
		return chunk.Response, nil
	}), nil
}

func wrapObservedEnhancedToolStream(ctx context.Context, stream *schema.StreamReader[*schema.ToolResult], callID string) (*schema.StreamReader[*schema.ToolResult], error) {
	cbStream := schema.StreamReaderWithConvert(stream, func(chunk *schema.ToolResult) (*einotool.CallbackOutput, error) {
		return &einotool.CallbackOutput{
			ToolOutput: chunk,
			Extra:      map[string]any{"tool_call_id": callID},
		}, nil
	})
	_, observed := callbacks.OnEndWithStreamOutput(ctx, cbStream)
	return schema.StreamReaderWithConvert(observed, func(chunk *einotool.CallbackOutput) (*schema.ToolResult, error) {
		if chunk == nil {
			return nil, nil
		}
		return chunk.ToolOutput, nil
	}), nil
}
