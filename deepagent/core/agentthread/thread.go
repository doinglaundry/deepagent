package agentthread

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	deepagents "eino-cli/deepagent/core"
	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/middleware"

	"github.com/cloudwego/eino/schema"
)

const (
	defaultRunEventBufferSize      = 4096
	threadEventForwardWarnDuration = 50 * time.Millisecond
)

type DeepAgentThread struct {
	ThreadID string

	runConfig *RunConfig
	cm        ContextManager
	evCh      chan Event

	mu      sync.Mutex
	current *run

	runIDProvider RunIDProvider
}

// Constructors and lifecycle

func New(
	threadID string,
	runConfig *RunConfig,
	eventBus chan Event,
	threadConfig ThreadOptions,
	opts ...Option,
) (thread *DeepAgentThread) {
	contextManager := threadConfig.ContextManager
	if contextManager == nil {
		contextManager = NewMemoryContextManager(
			threadID,
			threadConfig.HistoryStore,
			threadConfig.CompactionStrategy,
			threadConfig.TokenCounter,
			WithContextWindow(threadConfig.ContextWindow),
		)
	}
	if eventBus == nil {
		panic("agentthread: event bus is nil")
	}
	baseRunConfig := &RunConfig{}
	if runConfig != nil {
		baseRunConfig = runConfig.Clone()
	}
	thread = &DeepAgentThread{
		ThreadID:      threadID,
		runConfig:     baseRunConfig,
		cm:            contextManager,
		evCh:          eventBus,
		runIDProvider: defaultRunIDProvider,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(thread)
		}
	}
	return thread
}

// Init reloads persisted history before the thread starts processing messages.
// It is not safe to call concurrently with active run execution.
func (t *DeepAgentThread) Init(ctx context.Context) error {
	return t.cm.ReloadHistory(ctx)
}

func (t *DeepAgentThread) ContextManager() ContextManager {
	return t.cm
}

func (t *DeepAgentThread) Compact(ctx context.Context) (*ContextCompactedPayload, error) {
	return t.CompactWithRunID(ctx, t.newRunID(ctx, nil))
}

func (t *DeepAgentThread) CompactWithRunID(ctx context.Context, runID string) (*ContextCompactedPayload, error) {
	if runID == "" {
		return nil, ErrInvalidOp
	}
	if t.ActiveRun() != nil {
		return nil, ErrThreadRunning
	}
	return t.cm.Compact(ctx, runID)
}

// Input and run control

// ActiveRun returns the thread's currently active run, if any.
func (t *DeepAgentThread) ActiveRun() (handle *RunHandle) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return nil
	}
	handle = t.currentHandleLocked(t.current)
	return handle
}

// SubmitInput 向当前 Run 追加输入，或在没有 Run 时创建并启动一轮执行。
//
// 参数:
//   - ctx: 输入及新 Run 的上下文。
//   - input: 用户模型消息，不能为 nil。
//   - opts: 元数据、启动钩子与配置提供者。
//
// 返回值:
//   - result: 所属 Run ID、句柄及是否新建。
//   - err: 无效输入、当前 Run 已停止接收或构建失败。
//
// 功能特点:
//  1. 持锁选择追加或新建，防止同一 Thread 同时创建两轮执行。
//
// 执行流程:
//  1. 解析选项并加锁。
//  2. 有当前 Run 时入队并返回其句柄。
//  3. 否则创建 Run，解锁后启动执行。
//
// 使用示例:
//   - worker/thread 调用；Started=false 表示追加到已有 Run。
func (t *DeepAgentThread) SubmitInput(ctx context.Context, input *Message, opts ...SubmitInputOption) (result *SubmitInputResult, err error) {
	if input == nil {
		return nil, ErrInvalidOp
	}
	var submitOpts submitInputOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&submitOpts)
		}
	}
	t.mu.Lock()
	if t.current != nil {
		runID := t.current.runID
		if err := t.current.enqueueInput(input, submitOpts.InputMeta); err != nil {
			t.mu.Unlock()
			return nil, err
		}
		accepted := SubmitInputResult{
			RunID:     runID,
			RunHandle: t.currentHandleLocked(t.current),
		}
		t.mu.Unlock()
		return &accepted, nil
	}
	request := RunStartRequest{
		ThreadID:  t.ThreadID,
		RunID:     t.newRunID(ctx, input),
		Input:     input,
		InputMeta: submitOpts.InputMeta,
	}
	runCtx := applyRunStart(ctx, submitOpts.OnRunStart, request)
	current, err := t.startRunLocked(runCtx, request, submitOpts.ConfigProvider)
	if err != nil {
		t.mu.Unlock()
		return nil, err
	}
	accepted := SubmitInputResult{
		RunID:     current.runID,
		RunHandle: t.currentHandleLocked(current),
		Started:   true,
	}
	t.mu.Unlock()

	go t.executeRun(runCtx, current)
	return &accepted, nil
}

// ResumeRun resumes a checkpoint/interruption-bound run. Unlike SubmitInput,
// resume must target an existing run ID and is never queued into an active run.
func (t *DeepAgentThread) ResumeRun(ctx context.Context, runID string, opts ResumeRunOptions) (handle *RunHandle, err error) {
	if runID == "" || opts.CheckpointID == "" {
		return nil, ErrInvalidOp
	}
	t.mu.Lock()
	if t.current != nil {
		t.mu.Unlock()
		return nil, ErrThreadRunning
	}
	request := RunStartRequest{
		ThreadID: t.ThreadID,
		RunID:    runID,
		Resume:   copyResumeRunOptions(&opts),
	}
	runCtx := applyRunStart(ctx, opts.OnRunStart, request)
	current, err := t.startRunLocked(runCtx, request, opts.ConfigProvider)
	if err != nil {
		t.mu.Unlock()
		return nil, err
	}
	handle = t.currentHandleLocked(current)
	t.mu.Unlock()

	go t.executeRun(runCtx, current)
	return handle, nil
}

// Interrupt requests cancellation of the active run.
func (t *DeepAgentThread) Interrupt(opts InterruptOptions) (interrupted bool) {
	t.mu.Lock()
	current := t.current
	t.mu.Unlock()
	if current == nil {
		return false
	}
	return current.interrupt(opts)
}

// DrainInput removes pending inputs so the active run can process them.
func (t *DeepAgentThread) DrainInput(ctx context.Context) (messages []*schema.Message) {
	t.mu.Lock()
	current := t.current
	if current == nil {
		t.mu.Unlock()
		return nil
	}
	messages = current.drainInput()
	t.mu.Unlock()
	if len(messages) > 0 {
		current.events.emit(ctx, EventPendingInputProcessingStarted, PendingInputProcessingStartedPayload{
			Inputs: copyMessages(messages),
		})
	}
	return messages
}

// Run creation

func (t *DeepAgentThread) selectRunConfig(ctx context.Context, request RunStartRequest, provider RunConfigProvider) (runConfig *RunConfig, err error) {
	base := t.runConfig.Clone()
	if provider == nil {
		return base, nil
	}
	request.ThreadID = t.ThreadID
	request.Input = graph.CopyMessage(request.Input)
	request.Resume = copyResumeRunOptions(request.Resume)
	runConfig, err = provider(ctx, request)
	if err != nil {
		return nil, err
	}
	if runConfig == nil {
		return base, nil
	}
	return runConfig.Clone(), nil
}

// startRunLocked 构建并登记当前 Run，同时启动其事件转发。
//
// 参数:
//   - ctx: Run 上下文。
//   - request: 线程、Run 标识和输入。
//   - provider: 可选运行配置提供者。
//
// 返回值:
//   - current: 已构建但尚未在此执行模型的 Run。
//   - err: 标识、配置或构建失败。
//
// 功能特点:
//  1. 调用者必须持有 Thread 锁；构建成功后才设置 t.current。
//
// 执行流程:
//  1. 校验 Run ID 并选择配置。
//  2. 创建事件通道并构建 Run。
//  3. 登记 current，启动事件转发。
//
// 使用示例:
//   - SubmitInput 在没有当前 Run 时调用。
func (t *DeepAgentThread) startRunLocked(ctx context.Context, request RunStartRequest, provider RunConfigProvider) (current *run, err error) {
	if request.RunID == "" {
		return nil, ErrInvalidOp
	}
	runConfig, err := t.selectRunConfig(ctx, request, provider)
	if err != nil {
		return nil, err
	}
	current, err = t.buildRun(ctx, request, runConfig, make(chan Event, defaultRunEventBufferSize))
	if err != nil {
		return nil, err
	}
	t.current = current
	go t.forwardRunEvents(current)
	return current, nil
}

func (t *DeepAgentThread) buildRun(
	ctx context.Context,
	request RunStartRequest,
	runConfig *RunConfig,
	eventBus chan Event,
) (current *run, err error) {
	runID := request.RunID
	events := newRunEventRecorder(runConfig, t.ThreadID, runID, t.cm, eventBus)
	agentConfig := t.buildRunAgentConfig(ctx, runID, runConfig, events)
	workDir := ""
	if agentConfig.FilesystemConfig != nil {
		workDir = agentConfig.FilesystemConfig.WorkDir
	}
	slog.InfoContext(ctx, fmt.Sprintf("[agentthread::startRun] max_steps=%d max_model_calls=%d fs=%v web=%v workdir=%s skills_loader=%v hitl=%v", agentConfig.MaxSteps, agentConfig.MaxModelCalls, agentConfig.FilesystemConfig != nil, agentConfig.WebConfig != nil, workDir, agentConfig.SkillLoader != nil, agentConfig.HITLConfig != nil))
	agent, err := deepagents.New(ctx, deepagents.WithConfig(agentConfig))
	if err != nil {
		return nil, err
	}

	var onCompleted func(context.Context)
	if runConfig.RunCompleted != nil {
		completed := runConfig.RunCompleted
		chatModel := agentConfig.Model
		contextManager := t.cm
		threadID := t.ThreadID
		onCompleted = func(ctx context.Context) {
			history := append([]*schema.Message(nil), contextManager.History(ctx)...)
			completed(ctx, threadID, runID, chatModel, history)
		}
	}

	current = &run{
		threadID:       t.ThreadID,
		runID:          runID,
		input:          graph.CopyMessage(request.Input),
		resume:         copyResumeRunOptions(request.Resume),
		agent:          agent,
		events:         events,
		onCompleted:    onCompleted,
		acceptingInput: true,
		eventsDrained:  make(chan struct{}),
		done:           make(chan struct{}),
	}
	if request.Input != nil {
		current.consumed = append(current.consumed, graph.CopyMessage(request.Input))
		current.consumedInputMeta = append(current.consumedInputMeta, request.InputMeta)
	}
	return current, nil
}

func (t *DeepAgentThread) buildRunAgentConfig(
	ctx context.Context,
	runID string,
	runConfig *RunConfig,
	events *runEventRecorder,
) (agentConfig *deepagents.Config) {
	agentConfig = runConfig.Agent.Clone()
	agentConfig.ContextManager = &ctxMngMiddleware{
		core:    t.cm,
		runID:   runID,
		drainer: t,
		emit:    events.emit,
	}
	agentConfig.ContinueAfterModel = t.shouldContinue

	dynamicMiddlewares := []middleware.Middleware(nil)
	if runConfig.MiddlewaresProvider != nil {
		dynamicMiddlewares = runConfig.MiddlewaresProvider(ctx, runID)
	}
	agentConfig.Middlewares = slices.Concat(
		events.middlewares(runConfig.EnablePlan, agentConfig.ToolMask),
		dynamicMiddlewares,
		agentConfig.Middlewares,
	)
	agentConfig.Middlewares = slices.DeleteFunc(agentConfig.Middlewares, func(mw middleware.Middleware) bool {
		return mw == nil
	})

	if runConfig.CustomStateBuilder != nil {
		customStates := runConfig.CustomStateBuilder(ctx, t.ThreadID, runID)
		if len(customStates) > 0 {
			agentConfig.CustomGraphState = customStates
		}
	}

	return agentConfig
}

// Execution and event forwarding

// executeRun executes one logical run, drains all run-local events, and only then
// releases the thread's active-run slot. Keeping this sequence together makes
// the completion boundary explicit: a run is complete after execution and
// event forwarding have both finished.
func (t *DeepAgentThread) executeRun(ctx context.Context, current *run) {
	err := current.execute(ctx)
	current.events.close()
	<-current.eventsDrained
	t.finishRun(current, err)
}

// forwardRunEvents copies events from one agent execution into the thread-wide
// event channel and attaches the inputs consumed by that logical run.
func (t *DeepAgentThread) forwardRunEvents(current *run) {
	defer close(current.eventsDrained)
	eventBus := current.events.eventBus
	for event := range eventBus {
		event.ConsumedInputs, event.ConsumedInputsMeta = t.consumedInputsSnapshot(current)
		runQueueLen := len(eventBus)
		runQueueCap := cap(eventBus)
		threadQueueLen := len(t.evCh)
		threadQueueCap := cap(t.evCh)
		startedAt := time.Now()
		t.evCh <- event
		if elapsed := time.Since(startedAt); elapsed > threadEventForwardWarnDuration {
			slog.WarnContext(context.Background(), fmt.Sprintf("[DeepAgentThread::forwardRunEvents] slow event forward: thread_id=%s turn_id=%s event_type=%s elapsed=%s run_queue_len=%d run_queue_cap=%d thread_queue_len_before=%d thread_queue_cap=%d", t.ThreadID, current.runID, event.Type, elapsed, runQueueLen, runQueueCap, threadQueueLen, threadQueueCap))
		}
	}
}

// Completion and state helpers
func (t *DeepAgentThread) currentHandleLocked(current *run) (handle *RunHandle) {
	if current == nil {
		return nil
	}
	handle = &RunHandle{owner: t, run: current}
	return handle
}

func (t *DeepAgentThread) consumedInputsSnapshot(current *run) (messages []*schema.Message, metadata []any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if current == nil || len(current.consumed) == 0 {
		return nil, nil
	}
	messages = copyMessages(current.consumed)
	metadata = copyConsumedInputsMeta(current.consumedInputMeta)
	return messages, metadata
}

func (t *DeepAgentThread) newRunID(ctx context.Context, input *Message) string {
	if t.runIDProvider == nil {
		return defaultRunIDProvider(ctx, t.ThreadID, input)
	}
	if runID := t.runIDProvider(ctx, t.ThreadID, graph.CopyMessage(input)); runID != "" {
		return runID
	}
	return defaultRunIDProvider(ctx, t.ThreadID, input)
}

// finishRun closes the logical run and clears the thread's current run only
// when it is still the same run that was started.
func (t *DeepAgentThread) finishRun(current *run, runErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	current.runErr = runErr
	close(current.done)
	if t.current == current {
		t.current = nil
	}
}

func (t *DeepAgentThread) shouldContinue(context.Context) (continueRun bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return false, nil
	}
	if len(t.current.pending) > 0 {
		return true, nil
	}
	t.current.acceptingInput = false
	return false, nil
}
