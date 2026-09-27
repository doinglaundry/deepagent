package graph

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type DeepAgent struct {
	runID             string
	resourcesOpen     bool
	resourcesClosing  chan struct{}
	resourcesCloseErr error
	model             model.ToolCallingChatModel
	policy            tools.Policy
	eager             bool
	started           bool
	streamCancel      context.CancelFunc
	streamDone        chan struct{}
	middlewares       []middleware.Middleware
	graphState        *types.GraphState
	cfg               Config
	graph             compose.Runnable[*types.RunState, *schema.Message]
	conversation      Conversation
	registry          *tools.Registry
	executor          *toolExecutor
	emit              func(context.Context, types.RuntimeEvent) error
	drainInput        func(context.Context, string) ([]types.Input, bool, error)
	mu                sync.Mutex
	eventMu           sync.Mutex
	active            bool
	closed            bool
	cancel            context.CancelFunc
	interrupt         func(...compose.GraphInterruptOption)
	done              chan struct{}
	state             *types.RunState
	chunk             types.ModelChunkSink
}

func New(ctx context.Context, opts ...Option) (*DeepAgent, error) {
	cfg := Config{MaxSteps: 1000, Parallelism: 4, Name: "deepagent"}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.Model == nil {
		return nil, errors.New("model is required")
	}
	if cfg.Name == "" {
		cfg.Name = "deepagent"
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 1000
	}
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = 4
	}
	if cfg.MaxModelCalls < 0 {
		return nil, errors.New("max model calls must be >= 0")
	}
	err := validateSubAgents(cfg.SubAgents)
	if err != nil {
		return nil, err
	}
	history := cfg.Conversation
	if history == nil {
		history = conversation.New(cfg.ThreadID, nil, nil, nil)
	}
	a := &DeepAgent{cfg: cfg, emit: cfg.Emit, drainInput: cfg.DrainInput, conversation: history}
	a.runID = cfg.RunID
	if a.runID == "" {
		a.runID = uuid.NewString()
	}
	if err := a.configureRun(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *DeepAgent) Run(ctx context.Context, input []*schema.Message, opts ...RunOptionFunc) (*schema.Message, error) {
	return a.execute(ctx, input, nil, opts...)
}
func (a *DeepAgent) Stream(ctx context.Context, input []*schema.Message, opts ...RunOptionFunc) (*schema.StreamReader[*schema.Message], error) {
	raw, writer := schema.Pipe[*schema.Message](0)
	raw.SetAutomaticClose()
	streamCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if a.closed || a.active || a.streamDone != nil {
		a.mu.Unlock()
		cancel()
		raw.Close()
		writer.Close()
		return nil, errors.New("agent is closed or already running")
	}
	streamDone := make(chan struct{})
	a.streamDone = streamDone
	a.streamCancel = cancel
	a.mu.Unlock()
	stopClose := context.AfterFunc(streamCtx, raw.Close)
	chunks := make(chan *schema.Message)
	done := make(chan error, 1)
	options := append([]RunOptionFunc(nil), opts...)
	options = append(options, func(o *RunOptions) {
		o.streamDone = streamDone
		o.chunk = func(ctx context.Context, message *schema.Message) error {
			select {
			case chunks <- message:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	go func() { _, err := a.execute(streamCtx, input, nil, options...); done <- err }()
	go func() {
		defer func() {
			a.mu.Lock()
			writer.Close()
			a.streamCancel = nil
			a.streamDone = nil
			close(streamDone)
			a.mu.Unlock()
		}()
		defer cancel()
		defer stopClose()
		// This Eino version exposes consumer closure only through Send. A private
		// nil probe detects Close even while the provider has produced no tokens.
		// Conversion strips probes; consumers see only actual model/tool messages.
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case chunk := <-chunks:
				if writer.Send(chunk, nil) {
					cancel()
					<-done
					return
				}
			case err := <-done:
				if err != nil {
					writer.Send(nil, err)
				}
				return
			case <-ticker.C:
				if writer.Send(nil, nil) {
					cancel()
					<-done
					return
				}
			case <-streamCtx.Done():
				<-done
				return
			}
		}
	}()
	return schema.StreamReaderWithConvert(raw, func(message *schema.Message) (*schema.Message, error) {
		if message == nil {
			return nil, schema.ErrNoValue
		}
		return message, nil
	}), nil
}
func (a *DeepAgent) Close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	if a.streamCancel != nil {
		a.streamCancel()
	}
	var done chan struct{}
	if a.active {
		a.cancel()
		done = a.done
	}
	streamDone := a.streamDone
	a.mu.Unlock()
	for _, completion := range []chan struct{}{done, streamDone} {
		if completion == nil {
			continue
		}
		select {
		case <-completion:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return a.closeResources(ctx)
}
func (a *DeepAgent) Interrupt(opts ...compose.GraphInterruptOption) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active || a.interrupt == nil {
		return false
	}
	a.interrupt(opts...)
	// Eino's interrupt function closes a channel and is one-shot.
	a.interrupt = nil
	return true
}
func (a *DeepAgent) Name() string { return a.cfg.Name }
func (a *DeepAgent) Depth() int   { return a.cfg.Depth }
func (a *DeepAgent) event(ctx context.Context, state *types.RunState, kind, callID string, data any) error {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	state.EventSeq++
	event := types.RuntimeEvent{Sequence: state.EventSeq, Kind: kind, CallID: callID, Data: data}
	for _, mw := range a.middlewares {
		if observer, ok := mw.(middleware.EventObserver); ok {
			if err := observer.Observe(ctx, event); err != nil {
				return err
			}
		}
	}
	if a.emit == nil {
		return nil
	}
	return a.emit(ctx, event)
}

func (a *DeepAgent) GraphState() *types.GraphState { return a.graphState }

// GetGraphRunnable preserves the public Eino message-shaped interface. The
// adapter only converts inputs/options; all four methods use this agent's graph.
func (a *DeepAgent) GetGraphRunnable() compose.Runnable[[]*schema.Message, *schema.Message] {
	return messageRunnable{agent: a}
}

type messageRunnable struct{ agent *DeepAgent }

func (r messageRunnable) Invoke(ctx context.Context, input []*schema.Message, opts ...compose.Option) (*schema.Message, error) {
	return r.agent.Run(ctx, input, func(o *RunOptions) { o.composeOpts = append(o.composeOpts, opts...) })
}
func (r messageRunnable) Stream(ctx context.Context, input []*schema.Message, opts ...compose.Option) (*schema.StreamReader[*schema.Message], error) {
	return r.agent.Stream(ctx, input, func(o *RunOptions) { o.composeOpts = append(o.composeOpts, opts...) })
}
func (r messageRunnable) Collect(ctx context.Context, input *schema.StreamReader[[]*schema.Message], opts ...compose.Option) (*schema.Message, error) {
	messages, err := collectInputs(ctx, input)
	if err != nil {
		return nil, err
	}
	return r.Invoke(ctx, messages, opts...)
}
func (r messageRunnable) Transform(ctx context.Context, input *schema.StreamReader[[]*schema.Message], opts ...compose.Option) (*schema.StreamReader[*schema.Message], error) {
	messages, err := collectInputs(ctx, input)
	if err != nil {
		return nil, err
	}
	return r.Stream(ctx, messages, opts...)
}
func collectInputs(ctx context.Context, input *schema.StreamReader[[]*schema.Message]) ([]*schema.Message, error) {
	if input == nil {
		return nil, errors.New("input stream is nil")
	}
	defer input.Close()
	var messages []*schema.Message
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := input.Recv()
		if err == io.EOF {
			return messages, nil
		}
		if err != nil {
			return nil, err
		}
		messages = append(messages, part...)
	}
}
