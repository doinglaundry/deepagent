package thread

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
	inputpkg "eino-cli/deepagent/protocol/input"
	"eino-cli/deepagent/run"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

type Thread struct {
	closeMu              sync.Mutex
	closeResources       func(context.Context) error
	resourcesClosed      bool
	sessionID            string
	threadInfo           ContextThreadIdentity
	approvalRemember     ApprovalRememberer
	runFinishedObserver  RunFinishedObserver
	threadOutputObserver ThreadOutputObserver
	interruptResume      InterruptResumeDecoder
	observerQueue        chan ThreadOutputObservation
	observerOnce         sync.Once
	observerCancel       context.CancelFunc
	outputBridge         *threadOutputBridge
	compact              *compactOperation

	ThreadID          string
	mu                sync.Mutex
	current           *run.Run
	inputRuns         map[string]*run.Run // MessageID -> original Run, guarded by mu.
	pending           []types.Input
	conversation      execution.Conversation
	generateMessageID conversation.GetMessageIDFunc
	events            chan run.Event
	config            *run.Config
	closed            bool
	accepting         bool
}

// NewThread 创建内部状态并接管配置中的资源，不执行模型或加载历史。
func NewThread(cfg ThreadConfig) (*Thread, error) {
	if cfg.ThreadID == "" {
		return nil, fmt.Errorf("thread id is required")
	}
	events := cfg.Events
	if events == nil {
		events = make(chan run.Event, 256)
	}
	historyOptions := cfg.Options
	history := conversation.New(
		cfg.ThreadID,
		historyOptions.ConversationDB,
		historyOptions.Compactor,
		historyOptions.CountTokenFunc,
		historyOptions.ContextWindow,
		historyOptions.GenerateMessageID,
	)
	return &Thread{
		ThreadID:             cfg.ThreadID,
		sessionID:            cfg.SessionID,
		threadInfo:           ContextThreadIdentity{ThreadID: cfg.ThreadID, SessionID: cfg.SessionID, UserID: cfg.UserID},
		conversation:         history,
		generateMessageID:    historyOptions.GenerateMessageID,
		events:               events,
		config:               cfg.RunConfig.Clone(),
		inputRuns:            make(map[string]*run.Run),
		closeResources:       cfg.CloseResources,
		approvalRemember:     cfg.ApprovalRemember,
		runFinishedObserver:  cfg.RunFinishedObserver,
		threadOutputObserver: cfg.ThreadOutputObserver,
		interruptResume:      cfg.InterruptResume,
		outputBridge:         &threadOutputBridge{agentEvents: events},
	}, nil
}

func (t *Thread) Init(ctx context.Context) (*TransportThreadOutput, error) {
	ctx = t.withThreadInfo(ctx)
	err := t.ensureOpen()
	if err != nil {
		return nil, err
	}
	initHistoryErr := t.InitHistory(ctx)
	if initHistoryErr != nil {
		return nil, initHistoryErr
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, TransportErrThreadClosed
	}
	if t.outputBridge.output != nil {
		return t.outputBridge.output, nil
	}

	t.startThreadOutputObserver(ctx)
	return t.outputBridge.start(ctx, t), nil
}

func (t *Thread) InitHistory(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current != nil {
		return ErrThreadRunning
	}
	return t.conversation.ReloadHistory(ctx)
}

// PostMessage 将 Worker 消息按类型分派到输入、恢复或压缩入口。
func (t *Thread) PostMessage(ctx context.Context, message *TransportMessage) (posted *TransportPostMessageResult, err error) {
	ctx = t.withThreadInfo(ctx)
	if message == nil {
		return nil, fmt.Errorf("worker message is required")
	}
	err = t.ensureOpen()
	if err != nil {

		return nil, err
	}

	switch message.Type {
	case MessageTypeInput:
		cmd, err := decodeUserInputCommand(message)
		if err != nil {
			return nil, err
		}
		opts := []SubmitInputOption{WithMessageID(workerMessageID(cmd.message)), WithPlan(cmd.mode == inputpkg.UserMessageModeImplPlan)}
		if cmd.message != nil && len(cmd.message.Metadata) > 0 {
			opts = append(opts, WithInputMeta(maps.Clone(cmd.message.Metadata)))
		}
		opts = append(opts, WithRunStartHook(func(runCtx context.Context, req RunStartRequest) context.Context {
			return ContextContextWithRunIdentity(runCtx, ContextRunIdentity{
				ThreadID:  t.threadInfo.ThreadID,
				RunID:     req.RunID,
				MessageID: workerMessageID(cmd.message),
			})
		}))
		result, err := t.SubmitInput(ctx, cmd.inputMessage, opts...)
		if err != nil {
			return nil, fmt.Errorf("submit input: %w", err)
		}
		if result == nil {
			return nil, fmt.Errorf("submit input returned nil result")
		}
		return &TransportPostMessageResult{RunID: result.RunID}, nil
	case MessageTypeResumeRun:
		cmd, err := decodeResumeRunCommand(message)
		if err != nil {
			return nil, err
		}
		return t.postResumeRun(ctx, cmd)
	case MessageTypeCompact:
		cmd := decodeCompactCommand(message)
		err := t.postCompact(ctx, cmd)
		if err != nil {
			return nil, err
		}
		return &TransportPostMessageResult{RunID: cmd.runID}, nil
	default:
		return nil, unsupportedRuntimeCommand(message)
	}
}

func (t *Thread) SubmitInput(ctx context.Context, message *messagepkg.Message, opts ...SubmitInputOption) (*SubmitInputResult, error) {
	if message == nil {
		return nil, ErrInvalidOp
	}
	options := submitInputOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	clonedMessage := types.CopyMessage(message)
	if clonedMessage == nil {
		return nil, fmt.Errorf("failed to copy input message")
	}
	if options.MessageID != "" {
		if clonedMessage.MessageID != "" && clonedMessage.MessageID != options.MessageID {
			return nil, fmt.Errorf("input message identity mismatch")
		}
		clonedMessage.MessageID = options.MessageID
	}
	if clonedMessage.MessageID == "" && t.generateMessageID != nil {
		messageID, err := t.generateMessageID(ctx, clonedMessage)
		if err != nil {
			return nil, err
		}
		if messageID == "" {
			return nil, fmt.Errorf("message id provider returned an empty identity")
		}
		clonedMessage.MessageID = messageID
	}
	clonedMessage.ThreadID = t.ThreadID
	if clonedMessage.CreatedAt == 0 {
		clonedMessage.CreatedAt = time.Now().Unix()
	}
	input := types.Input{MessageID: clonedMessage.MessageID, Message: clonedMessage, Meta: options.InputMeta}
	for {
		err := ctx.Err()
		if err != nil {
			return nil, err
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil, ErrInvalidOp
		}
		if t.compact != nil {
			t.mu.Unlock()
			return nil, ErrThreadRunning
		}
		previous := t.inputRuns[input.MessageID]
		if previous != nil {
			result := &SubmitInputResult{RunID: previous.ID(), RunHandle: previous.Handle()}
			t.mu.Unlock()
			return result, nil
		}
		current := t.current
		if current != nil {
			if !t.accepting {
				done := current.Done()
				t.mu.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			input.Message.RunID = current.ID()
			t.pending = append(t.pending, input)
			if input.MessageID != "" {
				t.inputRuns[input.MessageID] = current
			}
			result := &SubmitInputResult{RunID: current.ID(), RunHandle: current.Handle()}
			t.mu.Unlock()
			return result, nil
		}
		id := uuid.NewString()
		input.Message.RunID = id
		request := RunStartRequest{ThreadID: t.ThreadID, RunID: id, Input: input.Message, InputMeta: input.Meta}
		r, runCtx, err := t.startRun(ctx, request, options.ConfigProvider, options.OnRunStart, options.EnablePlan)
		if err != nil {
			t.mu.Unlock()
			return nil, err
		}
		r.AddInputs(input)
		if input.MessageID != "" {
			t.inputRuns[input.MessageID] = r
		}
		t.current = r
		t.accepting = true
		t.mu.Unlock()
		go r.Execute(runCtx)
		return &SubmitInputResult{RunID: id, RunHandle: r.Handle(), Started: true}, nil
	}
}

func (t *Thread) startRun(ctx context.Context, request RunStartRequest, provider RunConfigProvider, hook OnRunStartFunc, enablePlan *bool) (*run.Run, context.Context, error) {
	cfg := *t.config
	if provider != nil {
		selected, err := provider(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		if selected == nil {
			return nil, nil, fmt.Errorf("nil Run config")
		}
		cfg = *selected
	}
	if enablePlan != nil {
		cfg.EnablePlan = *enablePlan
	}
	var unknownOutcome *checkpointer.ToolOutcomeUnknownError
	if request.Resume != nil {
		store := cfg.Graph.CheckpointStore
		if store == nil {
			return nil, nil, fmt.Errorf("resume requires checkpoint store")
		}
		if !request.Resume.ForceNewRun {
			store = checkpointer.New(store, t.ThreadID, request.RunID, "core-graph-v1")
		}
		snapshot, exists, err := store.Get(ctx, request.Resume.CheckpointID)
		unknown := errors.As(err, &unknownOutcome)
		if err != nil && !unknown {
			return nil, nil, fmt.Errorf("read resume checkpoint: %w", err)
		}
		if !exists && !unknown {
			return nil, nil, fmt.Errorf("resume checkpoint %q not found", request.Resume.CheckpointID)
		}
		if !request.Resume.ForceNewRun && !unknown {
			err := checkpointer.ValidateResume(snapshot, request.Resume.ResumeInterruptIDs, request.Resume.ResumeData)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	if hook != nil {
		updated := hook(ctx, request)
		if updated != nil {
			ctx = updated
		}
	}
	cfg.Graph.ThreadID = t.ThreadID
	cfg.Graph.RunID = request.RunID
	cfg.Graph.Conversation = t.conversation
	cfg.Graph.DrainInput = t.drainInput
	cfg.Events = t.events
	cfg.OnFinish = t.finishRun
	cfg.OnRestoredInputs = t.restoreInputOwnership
	if request.Resume != nil {
		cfg.Resume = &execution.RunOptions{
			CheckpointID:        request.Resume.CheckpointID,
			WriteToCheckpointID: request.Resume.WriteToCheckpointID,
			ForceNewRun:         request.Resume.ForceNewRun,
			ResumeInterruptIDs:  request.Resume.ResumeInterruptIDs,
			ResumeData:          request.Resume.ResumeData,
		}
	}
	r, runCtx := run.New(ctx, request.RunID, cfg)
	if unknownOutcome != nil {
		// Let execution publish failure for this Run. Its Graph still refuses
		// the unsafe snapshot; old queued inputs must retain this ownership.
		r.RestoreInputs(unknownOutcome.Inputs)
		for _, input := range unknownOutcome.Inputs {
			if input.MessageID != "" {
				t.inputRuns[input.MessageID] = r
			}
		}
	}
	return r, runCtx, nil
}

func (t *Thread) finishRun(ctx context.Context, r *run.Run, err error) error {
	request := r.InterruptRequest()
	timedOut := request != nil && context.Cause(ctx) == run.ErrExternalInterruptTimeout && errors.Is(err, context.Canceled)
	if timedOut {
		err = nil
	}
	// Close acceptance under the same lock as SubmitInput. Accepted pending
	// messages remain attributed to this Run even if execution failed.
	t.mu.Lock()
	t.accepting = false
	pending := r.AddInputs(t.pending...)
	t.pending = nil
	t.mu.Unlock()
	// Execution cancellation stops model/tools, but does not revoke delivery of
	// the terminal events. Keep the Run active until its event consumer accepts
	// them; Wait must not report completion ahead of this boundary.
	terminalCtx := context.WithoutCancel(ctx)
	info, interrupted := compose.ExtractInterruptInfo(err)
	if len(pending) > 0 {
		if interrupted {
			saveCtx, cancel := context.WithTimeout(terminalCtx, 10*time.Second)
			saveErr := r.PersistPending(saveCtx, pending)
			cancel()
			if saveErr != nil {
				err = fmt.Errorf("persist pending checkpoint inputs: %w", saveErr)
				interrupted = false
			}
		}
		// If checkpoint persistence fails, retain accepted inputs in history
		// before publishing the failed outcome.
		if !interrupted {
			saveCtx, cancel := context.WithTimeout(terminalCtx, 10*time.Second)
			for _, input := range pending {
				saveErr := t.conversation.AddHistory(saveCtx, r.ID(), input.Message)
				if saveErr != nil {
					err = errors.Join(err, saveErr)
					break
				}
				emitErr := r.PublishEvent(saveCtx, run.EventInputConsumed, input)
				if emitErr != nil {
					err = errors.Join(err, emitErr)
					break
				}
			}
			cancel()
		}
	}
	end := run.RunEndPayload{Status: "finished"}
	if interrupted {
		end.Status = "blocked"
		end.CheckpointID = r.CheckpointID()
		for _, interrupt := range info.InterruptContexts {
			if interrupt != nil && interrupt.ID != "" {
				end.InterruptID = interrupt.ID
				break
			}
		}
		if request != nil {
			end.Status = "interrupted"
		}
		err = r.EmitBlocked(terminalCtx, info)
	}
	if timedOut && err == nil {
		end.Status = "interrupted"
		payload := run.InterruptedPayload{Source: "external", Metadata: maps.Clone(request.Metadata)}
		if request.Timeout != nil {
			payload.TimeoutMS = request.Timeout.Milliseconds()
		}
		err = r.PublishEvent(terminalCtx, run.EventInterrupted, payload)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			end.Status = "interrupted"
		} else {
			end.Status = "failed"
		}
		_ = r.PublishEvent(terminalCtx, run.EventError, run.ErrorPayload{Message: err.Error(), Cancelled: errors.Is(err, context.Canceled)})
	}
	finalErr := r.PublishEvent(terminalCtx, run.EventRunEnd, end)
	if err == nil {
		err = finalErr
	}
	t.mu.Lock()
	if t.current == r {
		t.current = nil
	}
	t.mu.Unlock()
	return err
}

func (t *Thread) drainInput(_ context.Context, runID string) ([]types.Input, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil || t.current.ID() != runID {
		return nil, false, ErrNoActiveRun
	}
	if len(t.pending) == 0 {
		t.accepting = false
		return nil, false, nil
	}
	inputs := t.pending
	t.pending = nil
	inputs = t.current.AddInputs(inputs...)
	if len(inputs) == 0 {
		t.accepting = false
	}
	return inputs, len(inputs) > 0, nil
}

func (t *Thread) DrainInput(ctx context.Context) []*messagepkg.Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*messagepkg.Message
	if t.current == nil {
		return nil
	}
	for _, input := range t.pending {
		out = append(out, types.CopyMessage(input.Message))
	}
	t.current.AddInputs(t.pending...)
	t.pending = nil
	return out
}

func (t *Thread) CurrentRun() *run.Handle {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return nil
	}
	return t.current.Handle()
}

func (t *Thread) ActiveRun() *TransportActiveRun {
	if t == nil {
		return nil
	}
	compact := t.activeCompact()
	if compact != nil {
		return &TransportActiveRun{
			RunID:              compact.runID,
			ConsumedMessageIDs: append([]string(nil), compact.consumedMessageIDs...),
		}
	}
	curRun := t.CurrentRun()
	if curRun == nil {
		return nil
	}
	return &TransportActiveRun{
		RunID:              curRun.RunID(),
		ConsumedMessageIDs: ConsumedMessageIDs(curRun.ConsumedInputs()),
	}
}

func (t *Thread) ContextManager() ContextManager { return t.conversation }

func (t *Thread) Close(ctx context.Context) error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	t.mu.Lock()
	t.closed = true
	r := t.current
	if r != nil {
		t.accepting = false
		r.Cancel(context.Canceled)
	}
	bridge := t.outputBridge
	cancelObserver := t.observerCancel
	compact := t.compact
	t.mu.Unlock()
	if compact != nil {
		t.interruptCompact(ctx, TransportThreadInterruptRequest{Kind: TransportThreadInterruptKindCloseThread})
		select {
		case <-compact.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Execution must finish before its output bridge and filesystem stop.
	if r != nil {
		select {
		case <-r.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var err error
	if cancelObserver != nil {
		cancelObserver()
	}
	if bridge != nil {
		err = bridge.stopAndWait(ctx)
		if err != nil {
			return err
		}
	}
	if t.closeResources != nil && !t.resourcesClosed {
		err = t.closeResources(ctx)
		if err != nil {
			return err
		}
		t.resourcesClosed = true
	}
	return nil
}

func (t *Thread) withThreadInfo(ctx context.Context) context.Context {
	return ContextContextWithThreadIdentity(ctx, t.threadInfo)
}

func (t *Thread) ensureOpen() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return TransportErrThreadClosed
	}
	return nil
}

func (t *Thread) restoreInputOwnership(r *run.Run, inputs []types.Input) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.RestoreInputs(inputs)
	for _, input := range inputs {
		if input.MessageID != "" {
			t.inputRuns[input.MessageID] = r
		}
	}
}
