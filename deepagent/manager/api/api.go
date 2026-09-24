// Package api is the namespace-scoped Go facade shared by CLI and Workers.
package api

import (
	"context"
	"eino-cli/deepagent/protocol"
	memorypkg "eino-cli/deepagent/protocol/memory"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound   = memorypkg.ErrNotFound
	ErrConflict   = memorypkg.ErrConflict
	ErrPermitLost = errors.New("permit lost")
	ErrClosed     = errors.New("thread closed")
)

type State string

const (
	Idle    State = "idle"
	Ready   State = "ready"
	Running State = "running"
	Blocked State = "blocked"
	Closing State = "closing"
	Closed  State = "closed"
)

type Thread struct {
	ID        string          `json:"id"`
	Namespace string          `json:"namespace"`
	SessionID string          `json:"session_id"`
	ParentID  string          `json:"parent_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Role      string          `json:"role,omitempty"`
	WorkDir   string          `json:"work_dir"`
	State     State           `json:"state"`
	PlanMode  bool            `json:"plan_mode"`
	Block     *protocol.Block `json:"block,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}
type CreateThreadRequest struct {
	SessionID, ParentID, Name, Role, WorkDir string
	PlanMode                                 bool
	Input                                    *protocol.Input
}
type Permit struct {
	ThreadID, Token, WorkerID string
	ExpiresAt                 time.Time
}
type Claim struct {
	Thread Thread
	Permit Permit
	Inputs []protocol.Input
}
type Release struct{ Block *protocol.Block }
type EventFilter struct {
	ThreadID, SessionID string
	After               int64
	Limit               int
}
type Subscription struct {
	Events <-chan protocol.Event
	Errors <-chan error
	Close  func()
}
type History struct {
	// Rollout is the canonical Conversation record log when present. Messages
	// remains its effective-context projection for existing readers.
	Rollout     json.RawMessage
	Version     int64
	Messages    json.RawMessage
	Compactions json.RawMessage
}

// Manager methods are bound to a configured namespace, including subscriptions and checkpoints.
type Manager interface {
	CreateThread(context.Context, CreateThreadRequest) (Thread, error)
	GetThread(context.Context, string) (Thread, error)
	ListSessionThreads(context.Context, string, int, int) ([]Thread, error)
	SubmitInput(context.Context, string, protocol.Input) (protocol.Input, error)
	ResumeFromBlock(context.Context, string, protocol.Input) (protocol.Input, error)
	Cancel(context.Context, string, string) error
	RequestThreadClose(context.Context, string) error
	SetPlanMode(context.Context, string, bool) error
	ListEvents(context.Context, EventFilter) ([]protocol.Event, error)
	SubscribeSession(context.Context, string) (*Subscription, error)
	ScanRunnableThreads(context.Context, int) ([]Thread, error)
	ClaimThread(context.Context, string, string, time.Duration) (Claim, error)
	RenewThreadPermit(context.Context, Permit, time.Duration) (Permit, error)
	ReadPendingInputs(context.Context, Permit) ([]protocol.Input, error)
	ConfirmInputDelivery(context.Context, Permit, string) error
	ReleaseThread(context.Context, Permit, Release) error
	ConfirmThreadClosed(context.Context, Permit) error
	PublishEvent(context.Context, Permit, protocol.Event) (protocol.Event, error)
	LoadHistory(context.Context, string) (History, error)
	SaveHistory(context.Context, Permit, History) (History, error)
	GetCheckpoint(context.Context, string) ([]byte, error)
	PutCheckpoint(context.Context, string, []byte) error
}
