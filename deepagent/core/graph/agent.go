package graph

import (
	"context"
	"errors"
	"sync"

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

func (a *DeepAgent) Name() string { return a.cfg.Name }

func (a *DeepAgent) Depth() int { return a.cfg.Depth }

func (a *DeepAgent) GraphState() *types.GraphState { return a.graphState }

func (a *DeepAgent) Run(ctx context.Context, input []*schema.Message, opts ...RunOptionFunc) (*schema.Message, error) {
	return a.execute(ctx, input, opts...)
}
func (a *DeepAgent) Close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	var done chan struct{}
	if a.active {
		a.cancel()
		done = a.done
	}
	a.mu.Unlock()
	if done != nil {
		select {
		case <-done:
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
