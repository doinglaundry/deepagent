package types

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/schema"
)

// Input keeps the original multimodal message and its caller-owned identity metadata.
type Input struct {
	Message *schema.Message
	Meta    any
}

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

type Interrupt struct {
	InterruptID  string
	CallID       string
	CheckpointID string
	Kind         string
	Data         json.RawMessage
}
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// RunState is the serializable local state of the Eino graph. Live resources
// belong to DeepAgent, never to this checkpointed value.
type RunState struct {
	Version       int
	ThreadID      string
	RunID         string
	AgentName     string
	Depth         int
	Phase         Phase
	ModelCalls    int
	GraphSteps    int
	EventSeq      uint64
	HistoryCursor int64
	Consumed      []Input
	Calls         []ToolCallState
	Plan          []PlanStep
	Pending       []Interrupt
	Usage         Usage
	Extensions    map[string]json.RawMessage
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
