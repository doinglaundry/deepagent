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
func WithRunState(ctx context.Context, state *RunState) context.Context {
	return context.WithValue(ctx, runStateKey{}, state)
}
func RunStateFromContext(ctx context.Context) *RunState {
	state, _ := ctx.Value(runStateKey{}).(*RunState)
	return state
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
	Context        *ContextSnapshot
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
func (s RunState) MarshalJSON() ([]byte, error) { type wire RunState; return json.Marshal(wire(s)) }
func (s *RunState) UnmarshalJSON(raw []byte) error {
	type wire RunState
	return json.Unmarshal(raw, (*wire)(s))
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

func (as *GraphState) GetStateful(name string) RunTimeStateful {
	entry, ok := as.StateHolder[name]
	if !ok {
		return nil
	}
	return entry.stateful
}

func (as *GraphState) RegisterStateful(name string, stateful RunTimeStateful) {
	as.registerStatefulWithPersistence(name, stateful, true)
}

func (as *GraphState) RegisterRuntimeOnlyStateful(name string, stateful RunTimeStateful) {
	as.registerStatefulWithPersistence(name, stateful, false)
}

func (as *GraphState) registerStatefulWithPersistence(name string, stateful RunTimeStateful, persist bool) {
	as.StateHolder[name] = GraphStateEntry{
		stateful: stateful,
		persist:  persist,
	}
}

func (as *GraphState) RestoreExtensions(state *RunState) error {
	for name, entry := range as.StateHolder {
		raw, ok := state.Extensions["middleware:"+name]
		if !ok || !entry.persist {
			continue
		}
		var encoded string
		err := json.Unmarshal(raw, &encoded)
		if err != nil {
			return err
		}
		unmarshalRuntimeStateErr := entry.stateful.UnmarshalRuntimeState(encoded)
		if unmarshalRuntimeStateErr != nil {
			return unmarshalRuntimeStateErr
		}
	}
	return nil
}
func (as *GraphState) SnapshotExtensions(state *RunState) error {
	if state.Extensions == nil {
		state.Extensions = make(map[string]json.RawMessage)
	}
	for name, entry := range as.StateHolder {
		if !entry.persist {
			continue
		}
		raw, err := json.Marshal(entry.stateful.MarshalRuntimeState())
		if err != nil {
			return err
		}
		state.Extensions["middleware:"+name] = raw
	}
	return nil
}
