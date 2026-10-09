package model

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"

	"github.com/cloudwego/eino/schema"
)

// RunRecord is the durable execution result shared by scheduling and history.
// Eino checkpoint state remains responsible for restoring graph execution.
type RunRecord struct {
	RunID        string `gorm:"column:run_id;size:191;primaryKey"`
	ThreadID     int64  `gorm:"column:thread_id;index"`
	Status       string `gorm:"column:status;size:32"`
	LeaseToken   string `gorm:"column:lease_token;size:191" json:"-"`
	InterruptID  string `gorm:"column:interrupt_id;size:191"`
	CheckpointID string `gorm:"column:checkpoint_id"`
}

// RunInput keeps the original multimodal message and its delivery metadata.
type RunInput struct {
	MessageID string
	Message   *Message
	Meta      any
}

type persistedInput struct {
	MessageID string
	Message   *Message
	Meta      []byte
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

// RunState is the serializable local state of the Eino graph. Live resources
// belong to Graph, never to this checkpointed value.
type RunState struct {
	Version        int
	ThreadID       string
	RunID          string
	Depth          int
	Phase          Phase
	ModelCalls     int
	GraphSteps     int
	EventSeq       uint64
	PreparedInputs int
	HistorySeq     int64
	ContextUsage   *ContextTokenUsage
	Consumed       []RunInput
	Calls          []ToolCallState
	Plan           []PlanStep
	Pending        []Interrupt
	Usage          RunUsage
	Extensions     map[string]json.RawMessage
}

type RunUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// ContextTokenUsage 记录上下文占用估算及最近一次模型调用的输入、输出用量。
// JSON 字段名保留已有 checkpoint 格式，避免改名后续跑丢失用量。
type ContextTokenUsage struct {
	MaxContextTokens int64 `json:"ContextWindow"`             // 上下文容量上限
	TotalTokens      int64 `json:"CurrentTotal"`              // 当前上下文占用估算
	PromptTokens     int64 `json:"LastModelPromptTokens"`     // 最近一次模型的输入用量
	CompletionTokens int64 `json:"LastModelCompletionTokens"` // 最近一次模型的输出用量
}

func (RunRecord) TableName() string { return "agent_run" }

func (r *RunRecord) Ended() bool {
	return r != nil && (r.Status == "finished" || r.Status == "interrupted" || r.Status == "failed")
}

// AppendInputs keeps delivery identity stable across checkpoint restoration.
// Anonymous inputs have no identity and are always appended.
func AppendInputs(existing []RunInput, incoming ...RunInput) []RunInput {
	seen := make(map[string]bool, len(existing))
	for _, input := range existing {
		if input.MessageID != "" {
			seen[input.MessageID] = true
		}
	}
	for _, input := range incoming {
		if input.MessageID != "" && seen[input.MessageID] {
			continue
		}
		existing = append(existing, input)
		if input.MessageID != "" {
			seen[input.MessageID] = true
		}
	}
	return existing
}

// Metadata remains typed across checkpoint restore (in particular the
// Worker's map[string]string and 64-bit message identifiers).
func init() { gob.Register(map[string]string{}); gob.Register(map[string]any{}); gob.Register([]any{}) }

func (input RunInput) MarshalJSON() ([]byte, error) {
	var encoded bytes.Buffer
	if input.Meta != nil {
		err := gob.NewEncoder(&encoded).Encode(&input.Meta)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(persistedInput{MessageID: input.MessageID, Message: input.Message, Meta: encoded.Bytes()})
}

func (input *RunInput) UnmarshalJSON(raw []byte) error {
	var persisted persistedInput
	err := json.Unmarshal(raw, &persisted)
	if err != nil {
		return err
	}
	var metadata any
	if len(persisted.Meta) > 0 {
		err = gob.NewDecoder(bytes.NewReader(persisted.Meta)).Decode(&metadata)
		if err != nil {
			return err
		}
	}
	input.MessageID = persisted.MessageID
	input.Message = persisted.Message
	input.Meta = metadata
	return nil
}

func CopyMessage(message *Message) *Message {
	if message == nil {
		return nil
	}
	encodedMessage, err := json.Marshal(message)
	if err != nil {
		return nil
	}
	var copiedMessage Message
	if json.Unmarshal(encodedMessage, &copiedMessage) != nil {
		return nil
	}
	return &copiedMessage
}

// WithRunState exposes the graph-owned state to tools and middleware executing
// inside a node. It does not create or persist another state object.
func WithRunState(ctx context.Context, runState *RunState) context.Context {
	return context.WithValue(ctx, runStateKey{}, runState)
}

func GetRunState(ctx context.Context) *RunState {
	runState, _ := ctx.Value(runStateKey{}).(*RunState)
	return runState
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
