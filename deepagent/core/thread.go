package deepagents

import (
	"context"
	"fmt"
	"sync"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type RunConfig struct {
	MiddlewaresProvider func(context.Context, string) []middleware.Middleware
	Agent               Config
	EnablePlan          bool
	EventIDProvider     func(context.Context, string, string) string
	RunCompleted        func(context.Context, string, string, model.ToolCallingChatModel, []*schema.Message)
}
type ThreadOptions struct {
	ReplaceBootstrapPrompt bool
	HistoryStore           HistoryRolloutStore
	CompactionStrategy     CompactionStrategy
	TokenCounter           TokenCounter
	ContextWindow          int64
	HistoryRecordID        HistoryRecordIDProvider
}
type Input = types.Input
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

	ThreadID     string
	mu           sync.Mutex
	current      *Run
	inputRuns    map[string]*Run // MessageID -> original Run, guarded by mu.
	pending      []Input
	conversation Conversation
	events       chan Event
	config       *RunConfig
	closed       bool
}

// NewThread 创建内部状态并接管配置中的资源，不执行模型或加载历史。
func NewThread(cfg ThreadConfig) (*Thread, error) {
	if cfg.ThreadID == "" {
		return nil, fmt.Errorf("thread id is required")
	}
	events := cfg.Events
	if events == nil {
		events = make(chan Event, 256)
	}
	historyOptions := cfg.Options
	history := conversation.New(
		cfg.ThreadID, historyOptions.HistoryStore,
		historyOptions.CompactionStrategy, historyOptions.TokenCounter,
		conversation.WithContextWindow(historyOptions.ContextWindow),
		conversation.WithRecordID(historyOptions.HistoryRecordID),
		conversation.WithBootstrapPromptReplacement(historyOptions.ReplaceBootstrapPrompt),
	)
	return &Thread{
		ThreadID:             cfg.ThreadID,
		sessionID:            cfg.SessionID,
		threadInfo:           ContextThreadIdentity{ThreadID: cfg.ThreadID, SessionID: cfg.SessionID, UserID: cfg.UserID},
		conversation:         history,
		events:               events,
		config:               cfg.RunConfig.Clone(),
		inputRuns:            make(map[string]*Run),
		closeResources:       cfg.CloseResources,
		approvalRemember:     cfg.ApprovalRemember,
		runFinishedObserver:  cfg.RunFinishedObserver,
		threadOutputObserver: cfg.ThreadOutputObserver,
		interruptResume:      cfg.InterruptResume,
		outputBridge:         &threadOutputBridge{agentEvents: events},
	}, nil
}
func (t *Thread) InitHistory(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current != nil {
		return ErrThreadRunning
	}
	return t.conversation.ReloadHistory(ctx)
}
func (t *Thread) ContextManager() ContextManager { return t.conversation }
func (t *Thread) CurrentRun() *RunHandle {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return nil
	}
	return &RunHandle{run: t.current}
}

type SubmitInputResult struct {
	RunID     string     `json:"TurnID" yaml:"turnid"`
	RunHandle *RunHandle `json:"TurnHandle" yaml:"turnhandle"`
	Started   bool
}
type submitInputOptions struct {
	MessageID      string
	InputMeta      any
	EnablePlan     *bool
	ConfigProvider RunConfigProvider
	OnRunStart     OnRunStartFunc
}
type SubmitInputOption func(*submitInputOptions)

func WithMessageID(id string) SubmitInputOption {
	return func(o *submitInputOptions) { o.MessageID = id }
}

func WithInputMeta(meta any) SubmitInputOption {
	return func(o *submitInputOptions) { o.InputMeta = meta }
}
func WithPlan(enabled bool) SubmitInputOption {
	return func(o *submitInputOptions) { o.EnablePlan = &enabled }
}
func WithRunConfigProvider(provider RunConfigProvider) SubmitInputOption {
	return func(o *submitInputOptions) { o.ConfigProvider = provider }
}
func WithRunStartHook(hook OnRunStartFunc) SubmitInputOption {
	return func(o *submitInputOptions) { o.OnRunStart = hook }
}

// RunStartRequest is the existing public callback payload, not an execution layer.
type RunStartRequest struct {
	ThreadID  string
	RunID     string `json:"TurnID" yaml:"turnid"`
	Input     *Message
	InputMeta any
	Resume    *ResumeRunOptions
}
type RunConfigProvider func(context.Context, RunStartRequest) (*RunConfig, error)
type OnRunStartFunc func(context.Context, RunStartRequest) context.Context

type ResumeRunOptions struct {
	CheckpointID        string
	WriteToCheckpointID string
	ForceNewRun         bool
	EnablePlan          *bool
	ResumeInterruptIDs  []string
	ResumeData          map[string]any
	ConfigProvider      RunConfigProvider
	OnRunStart          OnRunStartFunc
}

func (t *Thread) SubmitInput(ctx context.Context, message *schema.Message, opts ...SubmitInputOption) (*SubmitInputResult, error) {
	if message == nil {
		return nil, ErrInvalidOp
	}
	options := submitInputOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	clonedMessage := CopyMessage(message)
	if clonedMessage == nil {
		return nil, fmt.Errorf("failed to copy input message")
	}
	input := Input{MessageID: options.MessageID, Message: clonedMessage, Meta: options.InputMeta}
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
			result := &SubmitInputResult{RunID: previous.runID, RunHandle: &RunHandle{run: previous}}
			t.mu.Unlock()
			return result, nil
		}
		current := t.current
		if current != nil {
			if !current.accepting {
				done := current.done
				t.mu.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			t.pending = append(t.pending, input)
			if input.MessageID != "" {
				t.inputRuns[input.MessageID] = current
			}
			result := &SubmitInputResult{RunID: current.runID, RunHandle: &RunHandle{run: current}}
			t.mu.Unlock()
			return result, nil
		}
		id := uuid.NewString()
		request := RunStartRequest{ThreadID: t.ThreadID, RunID: id, Input: input.Message, InputMeta: input.Meta}
		r, runCtx, err := t.startRun(ctx, request, options.ConfigProvider, options.OnRunStart, options.EnablePlan)
		if err != nil {
			t.mu.Unlock()
			return nil, err
		}
		r.consumed = []Input{input}
		if input.MessageID != "" {
			t.inputRuns[input.MessageID] = r
		}
		t.current = r
		t.mu.Unlock()
		go t.executeRun(runCtx, r)
		return &SubmitInputResult{RunID: id, RunHandle: &RunHandle{run: r}, Started: true}, nil
	}
}
func (t *Thread) startRun(ctx context.Context, request RunStartRequest, provider RunConfigProvider, hook OnRunStartFunc, enablePlan *bool) (*Run, context.Context, error) {
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
	if request.Resume != nil {
		store := cfg.Agent.CheckpointStore
		if store == nil {
			return nil, nil, fmt.Errorf("resume requires checkpoint store")
		}
		if !request.Resume.ForceNewRun {
			store = checkpointer.New(store, t.ThreadID, request.RunID, "core-graph-v1")
		}
		snapshot, exists, err := store.Get(ctx, request.Resume.CheckpointID)
		if err != nil {
			return nil, nil, fmt.Errorf("read resume checkpoint: %w", err)
		}
		if !exists {
			return nil, nil, fmt.Errorf("resume checkpoint %q not found", request.Resume.CheckpointID)
		}
		if !request.Resume.ForceNewRun {
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
	ctx, cancel := context.WithCancelCause(ctx)
	r := &Run{runID: request.RunID, owner: t, cancel: cancel, done: make(chan struct{}), accepting: true, config: cfg}
	return r, ctx, nil
}
func (t *Thread) drainInput(_ context.Context, runID string) ([]types.Input, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil || t.current.runID != runID {
		return nil, false, ErrNoActiveRun
	}
	if len(t.pending) == 0 {
		t.current.accepting = false
		return nil, false, nil
	}
	inputs := t.pending
	t.pending = nil
	t.current.mu.Lock()
	before := len(t.current.consumed)
	t.current.consumed = types.AppendInputs(t.current.consumed, inputs...)
	inputs = t.current.consumed[before:]
	t.current.mu.Unlock()
	if len(inputs) == 0 {
		t.current.accepting = false
	}
	return inputs, len(inputs) > 0, nil
}
func (t *Thread) DrainInput(ctx context.Context) []*schema.Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*schema.Message
	if t.current == nil {
		return nil
	}
	t.current.mu.Lock()
	defer t.current.mu.Unlock()
	for _, input := range t.pending {
		out = append(out, CopyMessage(input.Message))
		t.current.consumed = append(t.current.consumed, input)
	}
	t.pending = nil
	return out
}
func (t *Thread) ResumeRun(ctx context.Context, runID string, opts ResumeRunOptions) (*RunHandle, error) {
	if runID == "" || opts.CheckpointID == "" {
		return nil, ErrInvalidOp
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrInvalidOp
	}
	if t.current != nil || t.compact != nil {
		return nil, ErrThreadRunning
	}
	r, runCtx, err := t.startRun(ctx, RunStartRequest{ThreadID: t.ThreadID, RunID: runID, Resume: &opts}, opts.ConfigProvider, opts.OnRunStart, opts.EnablePlan)
	if err != nil {
		return nil, err
	}
	r.resume = &opts
	t.current = r
	go t.executeRun(runCtx, r)
	return &RunHandle{run: r}, nil
}

func (t *Thread) InterruptRun(opts InterruptOptions) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return false
	}
	t.current.requestInterrupt(opts)
	return true
}
func (t *Thread) Compact(ctx context.Context) (*ContextCompactedPayload, error) {
	return t.CompactWithRunID(ctx, uuid.NewString())
}
func (t *Thread) CompactWithRunID(ctx context.Context, runID string) (*ContextCompactedPayload, error) {
	if runID == "" {
		return nil, ErrInvalidOp
	}
	compactCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	op := &compactOperation{runID: runID, cancel: cancel, done: make(chan struct{})}
	if !t.beginCompact(op) {
		return nil, ErrThreadRunning
	}
	defer t.finishCompact(op)
	return t.conversation.Compact(compactCtx, runID)
}

func (c *RunConfig) Clone() *RunConfig {
	if c == nil {
		return &RunConfig{}
	}
	out := *c
	out.Agent = *c.Agent.Clone()
	return &out
}
