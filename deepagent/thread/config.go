package thread

import (
	"context"

	"eino-cli/deepagent/graph/conversation"
	agentmodel "eino-cli/deepagent/model"
	"eino-cli/deepagent/run"
)

type ThreadOptions struct {
	ConversationDB    agentmodel.ConversationDB
	Compactor         *conversation.SummaryCompaction
	CountTokenFunc    agentmodel.CountTokenFunc
	ContextWindow     int64
	GenerateMessageID agentmodel.GetMessageIDFunc
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
	Input     *agentmodel.Message
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

// ThreadConfig builds one Thread; there is no separate protocol adapter object.
type ThreadConfig struct {
	// CloseResources runs once after execution and output forwarding stop.
	CloseResources       func(context.Context) error
	SessionID            string
	ThreadID             string
	UserID               int64
	RunConfig            *run.Config
	Events               chan agentmodel.RunEvent
	Options              ThreadOptions
	ApprovalRemember     agentmodel.ApprovalRememberer
	RunFinishedObserver  agentmodel.RunFinishedObserver
	ThreadOutputObserver agentmodel.ThreadOutputObserver
	InterruptResume      agentmodel.InterruptResumeDecoder
}
