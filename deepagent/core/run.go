package deepagents

import (
	"context"
	"errors"
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

// Run owns one execution: Graph, tools, cancellation and completion.
// owner is nil for standalone and child runs.
type Run struct {
	err             error
	consumed        []Input
	owner           *Thread
	config          RunConfig
	accepting       bool // guarded by owner.mu
	resume          *ResumeRunOptions
	interruptOpts   *InterruptOptions
	interruptTimer  *time.Timer
	modelResponseID string

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
	tools             *tools.ToolSet
	executor          *toolExecutor
	emit              func(context.Context, types.RuntimeEvent) error
	drainInput        func(context.Context, string) ([]types.Input, bool, error)
	mu                sync.Mutex
	eventMu           sync.Mutex
	active            bool
	closed            bool
	cancel            context.CancelCauseFunc
	interrupt         func(...compose.GraphInterruptOption)
	done              chan struct{}
	state             *types.RunState
}

func NewRun(ctx context.Context, opts ...Option) (*Run, error) {
	cfg := Config{MaxSteps: 1000, Parallelism: 4, Name: "deepagent"}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	a := &Run{}
	err := a.initialize(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Run) initialize(ctx context.Context, cfg Config) error {
	if cfg.Model == nil {
		return errors.New("model is required")
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
		return errors.New("max model calls must be >= 0")
	}
	err := validateSubAgents(cfg.SubAgents)
	if err != nil {
		return err
	}
	history := cfg.Conversation
	if history == nil {
		history = conversation.New(cfg.ThreadID, nil, nil, nil)
	}
	a.cfg, a.emit, a.drainInput, a.conversation = cfg, cfg.Emit, cfg.DrainInput, history
	// A Thread publishes the identity before execution starts. Keep it immutable.
	if a.runID == "" {
		a.runID = cfg.RunID
		if a.runID == "" {
			a.runID = uuid.NewString()
		}
	}
	err = a.configureRun(ctx)
	if err != nil {
		return err
	}
	return nil
}

func (a *Run) Name() string { return a.cfg.Name }

func (a *Run) Depth() int { return a.cfg.Depth }

func (a *Run) GraphState() *types.GraphState { return a.graphState }

func (a *Run) Close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	var done chan struct{}
	if a.active {
		a.cancel(context.Canceled)
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
func (a *Run) Interrupt(opts ...compose.GraphInterruptOption) bool {
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

func (a *Run) event(ctx context.Context, state *types.RunState, kind, callID string, data any) error {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	state.EventSeq++
	event := types.RuntimeEvent{Sequence: state.EventSeq, Kind: kind, CallID: callID, Data: data}
	for _, mw := range a.middlewares {
		observer, ok := mw.(middleware.EventObserver)
		if ok {
			err := observer.Observe(ctx, event)
			if err != nil {
				return err
			}
		}
	}
	if a.emit == nil {
		return nil
	}
	return a.emit(ctx, event)
}

// complete is the sole completion boundary, after cleanup and terminal output.
func (a *Run) complete(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancel(err)
	a.err = err
	a.active = false
	a.interrupt = nil
	close(a.done)
}
