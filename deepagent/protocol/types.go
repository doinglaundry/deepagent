// Package protocol defines the transport-independent input and event contracts.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

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
type Resume struct {
	RunID        string `json:"run_id"`
	CheckpointID string `json:"checkpoint_id"`
	InterruptID  string `json:"interrupt_id"`
	Answer       string `json:"answer"`
	Approved     bool   `json:"approved"`
}
type Input struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"thread_id"`
	SessionID string    `json:"session_id"`
	Kind      InputKind `json:"kind"`
	Text      string    `json:"text,omitempty"`
	Parts     []Part    `json:"parts,omitempty"`
	Resume    *Resume   `json:"resume,omitempty"`
	CutoffID  string    `json:"cutoff_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (i Input) Validate() error {
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

type EventKind string

const (
	EventRunStarted    EventKind = "run_started"
	EventInputConsumed EventKind = "input_consumed"
	EventTextDelta     EventKind = "text_delta"
	EventText          EventKind = "text"
	EventToolStarted   EventKind = "tool_started"
	EventToolOutput    EventKind = "tool_output"
	EventToolCompleted EventKind = "tool_completed"
	EventPlan          EventKind = "plan"
	EventTokens        EventKind = "tokens"
	EventBlocked       EventKind = "blocked"
	EventRunCompleted  EventKind = "run_completed"
	EventRunFailed     EventKind = "run_failed"
	EventRunCancelled  EventKind = "run_cancelled"
	EventThreadClosed  EventKind = "thread_closed"
	EventCompacted     EventKind = "compacted"
)

type Block struct {
	RunID        string   `json:"run_id"`
	CheckpointID string   `json:"checkpoint_id"`
	InterruptID  string   `json:"interrupt_id"`
	Kind         string   `json:"kind"`
	Question     string   `json:"question"`
	ToolName     string   `json:"tool_name,omitempty"`
	Arguments    string   `json:"arguments,omitempty"`
	Options      []string `json:"options,omitempty"`
}
type Event struct {
	ID         string          `json:"id"`
	Sequence   int64           `json:"sequence"`
	Namespace  string          `json:"namespace"`
	SessionID  string          `json:"session_id"`
	ThreadID   string          `json:"thread_id"`
	RunID      string          `json:"run_id"`
	MessageIDs []string        `json:"message_ids,omitempty"`
	Kind       EventKind       `json:"kind"`
	ResponseID string          `json:"response_id,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Text       string          `json:"text,omitempty"`
	Arguments  string          `json:"arguments,omitempty"`
	Error      string          `json:"error,omitempty"`
	Block      *Block          `json:"block,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (e Event) Durable() bool { return e.Kind != EventTextDelta && e.Kind != EventToolOutput }
func (e Event) Terminal() bool {
	return e.Kind == EventRunCompleted || e.Kind == EventRunFailed || e.Kind == EventRunCancelled || e.Kind == EventBlocked
}
