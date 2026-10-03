package run

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/schema"
)

// Run owns the identity, accepted inputs and completion of one execution.
// Graph owns model/tool execution and its checkpoint state.
type Run struct {
	id              string
	config          Config
	mu              sync.Mutex
	graph           *execution.Graph
	consumed        []types.Input
	cancel          context.CancelCauseFunc
	done            chan struct{}
	err             error
	started         bool
	active          bool
	interruptOpts   *InterruptOptions
	interruptTimer  *time.Timer
	modelResponseID string
}

func New(ctx context.Context, id string, cfg Config) (*Run, context.Context) {
	ctx, cancel := context.WithCancelCause(ctx)
	return &Run{id: id, config: *cfg.Clone(), cancel: cancel, done: make(chan struct{}), active: true}, ctx
}

// Execute completes only after cleanup and Thread's terminal output are delivered.
func (r *Run) Execute(ctx context.Context) (result *schema.Message, err error) {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil, errors.New("run has already started")
	}
	r.started = true
	r.mu.Unlock()
	defer func() {
		if r.graph != nil {
			err = errors.Join(err, r.graph.Close(context.WithoutCancel(ctx)))
		}
		if err == nil && r.config.RunCompleted != nil {
			cfg := r.config.Graph
			r.config.RunCompleted(ctx, cfg.ThreadID, r.id, cfg.Model, cfg.Conversation.GetHistory(ctx))
		}
		r.mu.Lock()
		if r.interruptTimer != nil {
			r.interruptTimer.Stop()
		}
		r.mu.Unlock()
		if r.config.OnFinish != nil {
			err = r.config.OnFinish(ctx, r, err)
		}
		r.mu.Lock()
		r.cancel(err)
		r.err = err
		r.active = false
		close(r.done)
		r.mu.Unlock()
	}()

	cfg := *r.config.Graph.Clone()
	if r.config.EnablePlan {
		execution.WithPlan(nil)(&cfg)
	}
	cfg.RunID = r.id
	cfg.Emit = r.forwardGraphEvent
	compiledGraph, err := execution.New(ctx, execution.WithConfig(&cfg))
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.graph = compiledGraph
	requested := r.interruptOpts != nil
	r.mu.Unlock()
	// Requests made during configuration cannot be lost before Eino installs
	// its interrupt function. Cancellation also releases run-local resources.
	if requested {
		r.cancel(context.Canceled)
	}
	inputs := r.Inputs()
	messages := make([]*schema.Message, 0, len(inputs))
	options := execution.RunOptions{}
	if r.config.Resume != nil {
		options = *r.config.Resume
	} else if cfg.CheckpointStore != nil {
		options.CheckpointID = r.id
	}
	for _, input := range inputs {
		messages = append(messages, input.Message)
		options.InputIDs = append(options.InputIDs, input.MessageID)
		options.InputMeta = append(options.InputMeta, input.Meta)
	}
	var first *schema.Message
	if len(messages) > 0 {
		first = messages[0]
	}
	err = r.PublishEvent(ctx, EventRunStart, RunStartPayload{Input: first})
	if err != nil {
		return nil, err
	}
	return compiledGraph.Invoke(ctx, messages, func(target *execution.RunOptions) { *target = options })
}

func (r *Run) ID() string            { return r.id }
func (r *Run) Done() <-chan struct{} { return r.done }
func (r *Run) Cancel(cause error)    { r.cancel(cause) }

func (r *Run) Wait(ctx context.Context) error {
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Run) IsActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// AddInputs returns only the new inputs, preserving identity across redelivery.
func (r *Run) AddInputs(inputs ...types.Input) []types.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	before := len(r.consumed)
	r.consumed = types.AppendInputs(r.consumed, inputs...)
	return append([]types.Input(nil), r.consumed[before:]...)
}

func (r *Run) RestoreInputs(inputs []types.Input) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consumed = append([]types.Input(nil), inputs...)
}

func (r *Run) Inputs() []types.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.Input(nil), r.consumed...)
}

func (r *Run) CheckpointID() string {
	if r.config.Resume == nil {
		return r.id
	}
	if r.config.Resume.WriteToCheckpointID != "" {
		return r.config.Resume.WriteToCheckpointID
	}
	return r.config.Resume.CheckpointID
}

// PersistPending keeps Thread's accepted inputs in this Run's selected store.
func (r *Run) PersistPending(ctx context.Context, inputs []types.Input) error {
	return checkpointer.AppendInputs(ctx, r.config.Graph.CheckpointStore, r.CheckpointID(), r.config.Graph.ThreadID, r.id, inputs)
}

func (r *Run) RequestInterrupt(opts InterruptOptions) {
	r.mu.Lock()
	alreadyRequested := r.interruptOpts != nil
	request := InterruptOptions{Metadata: maps.Clone(opts.Metadata)}
	if opts.Timeout != nil {
		timeout := *opts.Timeout
		request.Timeout = &timeout
	}
	r.interruptOpts = &request
	if request.Timeout != nil && r.interruptTimer == nil {
		r.interruptTimer = time.AfterFunc(*request.Timeout, func() { r.cancel(ErrExternalInterruptTimeout) })
	}
	compiledGraph := r.graph
	r.mu.Unlock()
	if alreadyRequested {
		return
	}
	if compiledGraph == nil {
		r.cancel(context.Canceled)
		return
	}
	// Wait for the node boundary; forced snapshots can race with tool writes.
	interrupted := compiledGraph.Interrupt()
	if !interrupted {
		r.cancel(context.Canceled)
	}
}

func (r *Run) InterruptRequest() *InterruptOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.interruptOpts
}

var ErrExternalInterruptTimeout = fmt.Errorf("external interrupt timed out: %w", context.Canceled)
