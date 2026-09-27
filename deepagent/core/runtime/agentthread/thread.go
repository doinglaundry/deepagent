package agentthread

import (
	"context"
	"fmt"
	"sync"

	"eino-cli/deepagent/core/graph"
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
	Agent               graph.Config
	EnablePlan          bool
	EventIDProvider     func(context.Context, string, string) string
	RunCompleted        func(context.Context, string, string, model.ToolCallingChatModel, []*schema.Message)
}
type ThreadOptions struct {
	ReplaceBootstrapPrompt bool
	ContextManager         ContextManager
	HistoryStore           HistoryRolloutStore
	CompactionStrategy     CompactionStrategy
	TokenCounter           TokenCounter
	ContextWindow          int64
	HistoryRecordID        HistoryRecordIDProvider
}
type Input = types.Input
type DeepAgentThread struct {
	ThreadID      string
	mu            sync.Mutex
	current       *run
	pending       []Input
	conversation  graph.Conversation
	events        chan Event
	config        *RunConfig
	runIDProvider RunIDProvider
	compacting    bool
	closed        bool
}
type RunIDProvider func(context.Context, string, *Message) string
type Option func(*DeepAgentThread)

func WithRunIDProvider(provider RunIDProvider) Option {
	return func(t *DeepAgentThread) {
		if provider != nil {
			t.runIDProvider = provider
		}
	}
}
func New(threadID string, cfg *RunConfig, events chan Event, options ThreadOptions, opts ...Option) *DeepAgentThread {
	if events == nil {
		panic("agentthread: event bus is nil")
	}
	config := cfg.Clone()
	var history graph.Conversation
	if options.ContextManager != nil {
		var ok bool
		history, ok = options.ContextManager.(graph.Conversation)
		if !ok {
			history = &contextAdapter{ContextManager: options.ContextManager}
		}
	} else {
		history = conversation.New(threadID, options.HistoryStore, options.CompactionStrategy, options.TokenCounter, conversation.WithContextWindow(options.ContextWindow), conversation.WithRecordID(options.HistoryRecordID), conversation.WithBootstrapPromptReplacement(options.ReplaceBootstrapPrompt))
	}
	t := &DeepAgentThread{ThreadID: threadID, conversation: history, events: events, config: config, runIDProvider: func(context.Context, string, *Message) string { return uuid.NewString() }}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	return t
}
func (t *DeepAgentThread) Init(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current != nil {
		return ErrThreadRunning
	}
	return t.conversation.ReloadHistory(ctx)
}
func (t *DeepAgentThread) ContextManager() ContextManager { return t.conversation }
func (t *DeepAgentThread) ActiveRun() *RunHandle {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return nil
	}
	return &RunHandle{owner: t, run: t.current}
}

type SubmitInputResult struct {
	RunID     string     `json:"TurnID" yaml:"turnid"`
	RunHandle *RunHandle `json:"TurnHandle" yaml:"turnhandle"`
	Started   bool
}
type submitInputOptions struct {
	InputMeta      any
	ConfigProvider RunConfigProvider
	OnRunStart     OnRunStartFunc
}
type SubmitInputOption func(*submitInputOptions)

func WithInputMeta(meta any) SubmitInputOption {
	return func(o *submitInputOptions) { o.InputMeta = meta }
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
	ResumeInterruptIDs  []string
	ResumeData          map[string]any
	ConfigProvider      RunConfigProvider
	OnRunStart          OnRunStartFunc
}

func (t *DeepAgentThread) SubmitInput(ctx context.Context, message *schema.Message, opts ...SubmitInputOption) (*SubmitInputResult, error) {
	if message == nil {
		return nil, ErrInvalidOp
	}
	options := submitInputOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	input := Input{Message: graph.CopyMessage(message), Meta: options.InputMeta}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil, ErrInvalidOp
		}
		if t.compacting {
			t.mu.Unlock()
			return nil, ErrThreadRunning
		}
		if current := t.current; current != nil {
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
			result := &SubmitInputResult{RunID: current.id, RunHandle: &RunHandle{owner: t, run: current}}
			t.mu.Unlock()
			return result, nil
		}
		id := t.runIDProvider(ctx, t.ThreadID, input.Message)
		if id == "" {
			t.mu.Unlock()
			return nil, fmt.Errorf("empty run ID")
		}
		request := RunStartRequest{ThreadID: t.ThreadID, RunID: id, Input: input.Message, InputMeta: input.Meta}
		r, runCtx, err := t.startRun(ctx, request, options.ConfigProvider, options.OnRunStart)
		if err != nil {
			t.mu.Unlock()
			return nil, err
		}
		r.consumed = []Input{input}
		t.current = r
		t.mu.Unlock()
		go t.executeRun(runCtx, r)
		return &SubmitInputResult{RunID: id, RunHandle: &RunHandle{owner: t, run: r}, Started: true}, nil
	}
}
func (t *DeepAgentThread) startRun(ctx context.Context, request RunStartRequest, provider RunConfigProvider, hook OnRunStartFunc) (*run, context.Context, error) {
	cfg := *t.config
	if provider != nil {
		selected, err := provider(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		if selected == nil {
			return nil, nil, fmt.Errorf("nil run config")
		}
		cfg = *selected
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
			if err := checkpointer.ValidateResume(snapshot, request.Resume.ResumeInterruptIDs, request.Resume.ResumeData); err != nil {
				return nil, nil, err
			}
		}
	}
	if hook != nil {
		if updated := hook(ctx, request); updated != nil {
			ctx = updated
		}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	r := &run{id: request.RunID, owner: t, cancel: cancel, done: make(chan struct{}), accepting: true, config: cfg}
	return r, ctx, nil
}
func (t *DeepAgentThread) drainInput(_ context.Context, runID string) ([]types.Input, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil || t.current.id != runID {
		return nil, false, ErrNoActiveRun
	}
	if len(t.pending) == 0 {
		t.current.accepting = false
		return nil, false, nil
	}
	inputs := t.pending
	t.pending = nil
	t.current.mu.Lock()
	t.current.consumed = append(t.current.consumed, inputs...)
	t.current.mu.Unlock()
	return inputs, true, nil
}
func (t *DeepAgentThread) DrainInput(ctx context.Context) []*schema.Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*schema.Message
	if t.current == nil {
		return nil
	}
	t.current.mu.Lock()
	defer t.current.mu.Unlock()
	for _, input := range t.pending {
		out = append(out, graph.CopyMessage(input.Message))
		t.current.consumed = append(t.current.consumed, input)
	}
	t.pending = nil
	return out
}
func (t *DeepAgentThread) ResumeRun(ctx context.Context, runID string, opts ResumeRunOptions) (*RunHandle, error) {
	if runID == "" || opts.CheckpointID == "" {
		return nil, ErrInvalidOp
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrInvalidOp
	}
	if t.current != nil || t.compacting {
		return nil, ErrThreadRunning
	}
	r, runCtx, err := t.startRun(ctx, RunStartRequest{ThreadID: t.ThreadID, RunID: runID, Resume: &opts}, opts.ConfigProvider, opts.OnRunStart)
	if err != nil {
		return nil, err
	}
	r.resume = &opts
	t.current = r
	go t.executeRun(runCtx, r)
	return &RunHandle{owner: t, run: r}, nil
}

// Close stops the owned run and waits for its resource cleanup before returning.
func (t *DeepAgentThread) Close(ctx context.Context) error {
	t.mu.Lock()
	t.closed = true
	r := t.current
	if r != nil {
		r.accepting = false
		r.cancel(context.Canceled)
	}
	t.mu.Unlock()
	if r == nil {
		return nil
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *DeepAgentThread) Interrupt(opts InterruptOptions) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return false
	}
	t.current.interrupt(opts)
	return true
}
func (t *DeepAgentThread) Compact(ctx context.Context) (*ContextCompactedPayload, error) {
	return t.CompactWithRunID(ctx, uuid.NewString())
}
func (t *DeepAgentThread) CompactWithRunID(ctx context.Context, runID string) (*ContextCompactedPayload, error) {
	if runID == "" {
		return nil, ErrInvalidOp
	}
	t.mu.Lock()
	if t.current != nil || t.compacting {
		t.mu.Unlock()
		return nil, ErrThreadRunning
	}
	t.compacting = true
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.compacting = false; t.mu.Unlock() }()
	return t.conversation.Compact(ctx, runID)
}

// contextAdapter retains caller-owned history/context behavior while adding
// the Conversation run counter required by the graph.
type contextAdapter struct {
	ContextManager
	usage conversation.UsageTracker
}

func (c *contextAdapter) RunUsage() types.Usage { return c.usage.RunUsage() }
func (c *contextAdapter) RestoreRunUsage(ctx context.Context, usage types.Usage) error {
	return c.usage.RestoreRunUsage(ctx, usage)
}
func (c *contextAdapter) RecordModelUsage(ctx context.Context, usage *model.TokenUsage) {
	c.ContextManager.RecordModelUsage(ctx, usage)
	c.usage.RecordRunUsage(usage)
}

func (c *contextAdapter) BuildRequest(ctx context.Context, prompts []*schema.Message) ([]*schema.Message, error) {
	if builder, ok := c.ContextManager.(interface {
		BuildRequest(context.Context, []*schema.Message) ([]*schema.Message, error)
	}); ok {
		return builder.BuildRequest(ctx, prompts)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append(append([]*schema.Message(nil), prompts...), c.History(ctx)...), nil
}

func (c *RunConfig) Clone() *RunConfig {
	if c == nil {
		return &RunConfig{}
	}
	out := *c
	out.Agent = *c.Agent.Clone()
	return &out
}
