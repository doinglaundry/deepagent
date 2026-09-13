package agentthread

import "context"

// ThreadOptions contains the context and persistence dependencies used to
// create a DeepAgentThread.
type ThreadOptions struct {
	// ContextManager replaces the built-in memory context manager when non-nil.
	ContextManager     ContextManager
	HistoryStore       HistoryRolloutStore
	CompactionStrategy CompactionStrategy
	TokenCounter       TokenCounter
	ContextWindow      int64
}

// ResumeRunOptions configures one checkpoint/interruption resume run.
type ResumeRunOptions struct {
	CheckpointID        string
	WriteToCheckpointID string
	ForceNewRun         bool
	ResumeInterruptIDs  []string
	ResumeData          map[string]any
	ConfigProvider      RunConfigProvider
	OnRunStart          OnRunStartFunc `json:"OnTurnStart" yaml:"onturnstart"`
}

// SubmitInputResult describes how one user input was accepted by the thread.
type SubmitInputResult struct {
	RunID     string     `json:"TurnID" yaml:"turnid"`
	RunHandle *RunHandle `json:"TurnHandle" yaml:"turnhandle"`
	Started   bool
}
type submitInputOptions struct {
	ConfigProvider RunConfigProvider
	InputMeta      any
	OnRunStart     OnRunStartFunc `json:"OnTurnStart" yaml:"onturnstart"`
}
type SubmitInputOption func(*submitInputOptions)

func WithRunConfigProvider(provider RunConfigProvider) (option SubmitInputOption) {
	option = func(opts *submitInputOptions) { opts.ConfigProvider = provider }
	return option
}
func WithInputMeta(meta any) (option SubmitInputOption) {
	option = func(opts *submitInputOptions) { opts.InputMeta = meta }
	return option
}

type RunIDProvider func(ctx context.Context, threadID string, input *Message) string
type Option func(*DeepAgentThread)

func WithRunIDProvider(provider RunIDProvider) (option Option) {
	option = func(t *DeepAgentThread) {
		if provider != nil {
			t.runIDProvider = provider
		}
	}
	return option
}
