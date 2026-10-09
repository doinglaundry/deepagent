package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type InputKind string

const (
	InputUser    InputKind = "user"
	InputResume  InputKind = "resume"
	InputCancel  InputKind = "cancel"
	InputClose   InputKind = "close"
	InputCompact InputKind = "compact"
)

type Part struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

type ProtocolResume struct {
	// Kind is stamped by Manager from the correlated durable block.
	Kind         string                 `json:"kind,omitempty"`
	RunID        string                 `json:"run_id"`
	CheckpointID string                 `json:"checkpoint_id"`
	InterruptID  string                 `json:"interrupt_id"`
	Answer       string                 `json:"answer"`
	Approved     bool                   `json:"approved"`
	Answers      []ProtocolResumeAnswer `json:"answers,omitempty"`
	Items        []BlockItem            `json:"items,omitempty"`
}

type ProtocolResumeAnswer struct {
	InterruptID string `json:"interrupt_id"`
	Approved    bool   `json:"approved"`
	Answer      string `json:"answer,omitempty"`
}

type ProtocolInput struct {
	ID        string          `json:"id"`
	ThreadID  string          `json:"thread_id"`
	SessionID string          `json:"session_id"`
	Kind      InputKind       `json:"kind"`
	Text      string          `json:"text,omitempty"`
	Parts     []Part          `json:"parts,omitempty"`
	Resume    *ProtocolResume `json:"resume,omitempty"`
	CutoffID  string          `json:"cutoff_id,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type ProtocolEventKind string

const (
	EventRunStarted            ProtocolEventKind = "run_started"
	ProtocolEventInputConsumed ProtocolEventKind = "input_consumed"
	EventTextDelta             ProtocolEventKind = "text_delta"
	EventText                  ProtocolEventKind = "text"
	EventToolStarted           ProtocolEventKind = "tool_started"
	EventToolOutput            ProtocolEventKind = "tool_output"
	EventToolCompleted         ProtocolEventKind = "tool_completed"
	EventPlan                  ProtocolEventKind = "plan"
	ProtocolEventTokens        ProtocolEventKind = "tokens"
	EventBlocked               ProtocolEventKind = "blocked"
	EventRunCompleted          ProtocolEventKind = "run_completed"
	EventRunFailed             ProtocolEventKind = "run_failed"
	EventRunCancelled          ProtocolEventKind = "run_cancelled"
	EventThreadClosed          ProtocolEventKind = "thread_closed"
	EventCompacted             ProtocolEventKind = "compacted"
)

type Block struct {
	RunID        string      `json:"run_id"`
	CheckpointID string      `json:"checkpoint_id"`
	InterruptID  string      `json:"interrupt_id"`
	Kind         string      `json:"kind"`
	Question     string      `json:"question"`
	ToolName     string      `json:"tool_name,omitempty"`
	Arguments    string      `json:"arguments,omitempty"`
	Options      []string    `json:"options,omitempty"`
	Items        []BlockItem `json:"items,omitempty"`
}

type BlockItem struct {
	InterruptID string `json:"interrupt_id"`
	Kind        string `json:"kind"`
	ToolName    string `json:"tool_name,omitempty"`
	Arguments   string `json:"arguments,omitempty"`
	Question    string `json:"question,omitempty"`
}

type ProtocolEvent struct {
	ID         string            `json:"id"`
	Sequence   int64             `json:"sequence"`
	Namespace  string            `json:"namespace"`
	SessionID  string            `json:"session_id"`
	ThreadID   string            `json:"thread_id"`
	RunID      string            `json:"run_id"`
	MessageIDs []string          `json:"message_ids,omitempty"`
	Kind       ProtocolEventKind `json:"kind"`
	ResponseID string            `json:"response_id,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolName   string            `json:"tool_name,omitempty"`
	Text       string            `json:"text,omitempty"`
	Arguments  string            `json:"arguments,omitempty"`
	Error      string            `json:"error,omitempty"`
	Block      *Block            `json:"block,omitempty"`
	Data       json.RawMessage   `json:"data,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

func (i ProtocolInput) Validate() error {
	switch i.Kind {
	case InputUser:
		if strings.TrimSpace(i.Text) == "" && len(i.Parts) == 0 {
			return fmt.Errorf("input text or parts required")
		}
	case InputResume:
		if i.Resume == nil || i.Resume.RunID == "" || i.Resume.CheckpointID == "" || i.Resume.InterruptID == "" {
			return fmt.Errorf("resume requires run, checkpoint and interrupt identifiers")
		}
	case InputCancel, InputClose, InputCompact:
	default:
		return fmt.Errorf("unknown input kind %q", i.Kind)
	}
	return nil
}

func (e ProtocolEvent) Durable() bool { return e.Kind != EventTextDelta && e.Kind != EventToolOutput }

func (e ProtocolEvent) Terminal() bool {
	return e.Kind == EventRunCompleted || e.Kind == EventRunFailed || e.Kind == EventRunCancelled || e.Kind == EventBlocked
}
