package model

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/schema"
)

// Middleware is the lifecycle contract for one graph middleware.
// BaseMiddleware supplies no-op implementations for optional hooks.
type Middleware interface {
	GetName() string
	PrepareAgent(context.Context) error
	GetStateHandler() RunTimeStateful
	BuildPrompt(context.Context) ([]*Message, error)
	ModifyModelRequest(context.Context, []*Message, []*Message, *GraphState) ([]*Message, error)
	ModifyModelResponse(context.Context, *Message, *GraphState) (*Message, error)
	ModifyModelStreamResponse(context.Context, *schema.StreamReader[*Message], *GraphState) (*schema.StreamReader[*Message], error)
}

// Optional capabilities are detected directly by Graph; there is no second
// middleware pipeline or intermediate execution object.
type RunMiddleware interface {
	PrepareRun(context.Context, *RunState) error
	FinishRun(context.Context, *RunState, error) error
}

type EventObserver interface {
	Observe(context.Context, RuntimeEvent) error
}

type ModelHandler func(context.Context, []*Message) (*schema.StreamReader[*Message], error)

type ModelMiddleware interface {
	WrapModel(ModelHandler) ModelHandler
}

// RunFactory creates fresh mutable state when a configured middleware is reused.
type RunFactory interface{ NewRun() Middleware }

// ResourceCloser releases resources acquired during setup or execution. Close must tolerate construction without PrepareRun.
type ResourceCloser interface{ Close(context.Context) error }

// RunTimeStateful serializes and restores a component's runtime state.
type RunTimeStateful interface {
	MarshalRuntimeState() string
	UnmarshalRuntimeState(data string) error
}

type GraphStateEntry struct {
	persist  bool
	stateful RunTimeStateful
}

// GraphState registers live middleware state persisted within RunState.Extensions.
type GraphState struct {
	StateHolder map[string]GraphStateEntry
}

func NewGraphState() *GraphState {
	return &GraphState{StateHolder: make(map[string]GraphStateEntry)}
}

func (graphState *GraphState) GetStateful(name string) RunTimeStateful {
	stateEntry, ok := graphState.StateHolder[name]
	if !ok {
		return nil
	}
	return stateEntry.stateful
}

func (graphState *GraphState) RegisterStateful(name string, stateful RunTimeStateful) {
	graphState.registerStatefulWithPersistence(name, stateful, true)
}

func (graphState *GraphState) RegisterRuntimeOnlyStateful(name string, stateful RunTimeStateful) {
	graphState.registerStatefulWithPersistence(name, stateful, false)
}

func (graphState *GraphState) registerStatefulWithPersistence(name string, stateful RunTimeStateful, persist bool) {
	graphState.StateHolder[name] = GraphStateEntry{
		stateful: stateful,
		persist:  persist,
	}
}

func (graphState *GraphState) RestoreExtensions(runState *RunState) error {
	for name, stateEntry := range graphState.StateHolder {
		raw, ok := runState.Extensions["middleware:"+name]
		if !ok || !stateEntry.persist {
			continue
		}
		var encoded string
		err := json.Unmarshal(raw, &encoded)
		if err != nil {
			return err
		}
		unmarshalRuntimeStateErr := stateEntry.stateful.UnmarshalRuntimeState(encoded)
		if unmarshalRuntimeStateErr != nil {
			return unmarshalRuntimeStateErr
		}
	}
	return nil
}

func (graphState *GraphState) SnapshotExtensions(runState *RunState) error {
	if runState.Extensions == nil {
		runState.Extensions = make(map[string]json.RawMessage)
	}
	for name, stateEntry := range graphState.StateHolder {
		if !stateEntry.persist {
			continue
		}
		raw, err := json.Marshal(stateEntry.stateful.MarshalRuntimeState())
		if err != nil {
			return err
		}
		runState.Extensions["middleware:"+name] = raw
	}
	return nil
}
