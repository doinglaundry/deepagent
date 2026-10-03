package thread

import (
	"context"

	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/types"
	inputpkg "eino-cli/deepagent/protocol/input"
	"eino-cli/deepagent/run"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ContextManager preserves the Thread context contract.
type ContextManager interface {
	ReloadHistory(context.Context) error
	AddHistory(context.Context, string, ...*schema.Message) error
	History(context.Context) []*schema.Message
	ContextUsage() types.ContextUsageSnapshot
	RecordModelUsage(context.Context, *model.TokenUsage)
	Compact(context.Context, string) (*conversation.ContextCompactedPayload, error)
	CompactNeeded(context.Context) bool
}

type ThreadOptions struct {
	ReplaceBootstrapPrompt bool
	HistoryStore           conversation.HistoryRolloutStore
	CompactionStrategy     conversation.CompactionStrategy
	TokenCounter           conversation.TokenCounter
	ContextWindow          int64
	HistoryRecordID        conversation.HistoryRecordIDProvider
}

type SubmitInputResult struct {
	RunID     string      `json:"TurnID" yaml:"turnid"`
	RunHandle *run.Handle `json:"TurnHandle" yaml:"turnhandle"`
	Started   bool
}

type submitInputOptions struct {
	MessageID      string
	InputMeta      any
	EnablePlan     *bool
	ConfigProvider RunConfigProvider
	OnRunStart     OnRunStartFunc
}

type SubmitInputOption func(*submitInputOptions)

func WithMessageID(id string) SubmitInputOption {
	return func(o *submitInputOptions) { o.MessageID = id }
}

func WithInputMeta(meta any) SubmitInputOption {
	return func(o *submitInputOptions) { o.InputMeta = meta }
}

func WithPlan(enabled bool) SubmitInputOption {
	return func(o *submitInputOptions) { o.EnablePlan = &enabled }
}

func WithRunConfigProvider(provider RunConfigProvider) SubmitInputOption {
	return func(o *submitInputOptions) { o.ConfigProvider = provider }
}

func WithRunStartHook(hook OnRunStartFunc) SubmitInputOption {
	return func(o *submitInputOptions) { o.OnRunStart = hook }
}

// RunStartRequest is the existing public callback payload, not an execution layer.
type RunStartRequest struct {
	ThreadID  string
	RunID     string `json:"TurnID" yaml:"turnid"`
	Input     *schema.Message
	InputMeta any
	Resume    *ResumeRunOptions
}

type RunConfigProvider func(context.Context, RunStartRequest) (*run.Config, error)

type OnRunStartFunc func(context.Context, RunStartRequest) context.Context

type ResumeRunOptions struct {
	CheckpointID        string
	WriteToCheckpointID string
	ForceNewRun         bool
	EnablePlan          *bool
	ResumeInterruptIDs  []string
	ResumeData          map[string]any
	ConfigProvider      RunConfigProvider
	OnRunStart          OnRunStartFunc
}

// ApprovalRememberer records a session-scoped approval reuse decision.
type ApprovalRememberer interface {
	RememberApproval(ctx context.Context, payload inputpkg.ResumeRunPayload)
}

type ApprovalRemembererFunc func(ctx context.Context, payload inputpkg.ResumeRunPayload)

func (f ApprovalRemembererFunc) RememberApproval(ctx context.Context, payload inputpkg.ResumeRunPayload) {
	f(ctx, payload)
}

// RunFinishedObserver is called after a Run run-end event is converted
// to worker output. Implementations should return quickly.
type RunFinishedObserver func(ctx context.Context, ev run.Event)

// ThreadOutputObservation is a read-only snapshot of one worker output item
// emitted by the Thread runtime.
type ThreadOutputObservation struct {
	SessionID string
	ThreadID  string
	Item      TransportThreadOutputItem
}

// ThreadOutputObserver is called after the Thread runtime has
// successfully offered one output item to the worker host. Implementations
// should return quickly and must not rely on mutating the observed item.
type ThreadOutputObserver func(ctx context.Context, obs ThreadOutputObservation)

// InterruptResumeDecoder converts a generic Run interrupt resume payload
// into the typed data expected by a custom Eino interrupt handler.
type InterruptResumeDecoder func(ctx context.Context, payload inputpkg.ResumeRunPayload) (any, error)

// ThreadConfig builds one Thread; there is no separate protocol adapter object.
type ThreadConfig struct {
	// CloseResources runs once after execution and output forwarding stop.
	CloseResources       func(context.Context) error
	SessionID            string
	ThreadID             string
	UserID               int64
	RunConfig            *run.Config
	Events               chan run.Event
	Options              ThreadOptions
	ApprovalRemember     ApprovalRememberer
	RunFinishedObserver  RunFinishedObserver
	ThreadOutputObserver ThreadOutputObserver
	InterruptResume      InterruptResumeDecoder
}
