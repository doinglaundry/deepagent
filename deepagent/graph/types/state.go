package types

import (
	"context"
	"encoding/json"
	"github.com/cloudwego/eino/schema"
)

type Phase string

const (
	PhasePreparing   Phase = "preparing"
	PhaseModeling    Phase = "modeling"
	PhaseTools       Phase = "tools"
	PhaseBlocked     Phase = "blocked"
	PhaseCompleted   Phase = "completed"
	PhaseInterrupted Phase = "interrupted"
	PhaseFailed      Phase = "failed"
)

type PlanStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

type runStateKey struct{}

// WithRunState exposes the graph-owned state to tools and middleware executing
// inside a node. It does not create or persist another state object.
func WithRunState(ctx context.Context, runState *RunState) context.Context {
	return context.WithValue(ctx, runStateKey{}, runState)
}
func GetRunState(ctx context.Context) *RunState {
	runState, _ := ctx.Value(runStateKey{}).(*RunState)
	return runState
}

// RunState is the serializable local state of the Eino graph. Live resources
// belong to Graph, never to this checkpointed value.
type RunState struct {
	Version        int
	ThreadID       string
	RunID          string
	AgentName      string
	Depth          int
	Phase          Phase
	ModelCalls     int
	GraphSteps     int
	EventSeq       uint64
	PreparedInputs int
	HistorySeq     int64
	ContextUsage   *ContextTokenUsage
	Consumed       []Input
	Calls          []ToolCallState
	Plan           []PlanStep
	Pending        []Interrupt
	Usage          Usage
	Extensions     map[string]json.RawMessage
}

func init() { schema.RegisterName[*RunState]("deepagent_run_state_v1") }

// Eino's reflective serializer in the pinned version does not round-trip
// json.RawMessage map values. Encoding this state as one JSON value preserves
// extension payloads while leaving the Eino checkpoint as the only snapshot.
func (runState RunState) MarshalJSON() ([]byte, error) {
	type wire RunState
	return json.Marshal(wire(runState))
}
func (runState *RunState) UnmarshalJSON(raw []byte) error {
	type wire RunState
	return json.Unmarshal(raw, (*wire)(runState))
}

type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

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
