package session

import "time"

type CommandInputType string

type CommandStatus string

const (
	CommandInputTypeNaturalLanguage CommandInputType = "natural_language"
	CommandInputTypeSlashCommand    CommandInputType = "slash_command"

	CommandStatusAccepted  CommandStatus = "accepted"
	CommandStatusRunning   CommandStatus = "running"
	CommandStatusCompleted CommandStatus = "completed"
	CommandStatusFailed    CommandStatus = "failed"
)

type Session struct {
	ID            string    `json:"id"`
	WorkspaceRoot string    `json:"workspace_root"`
	StartedAt     time.Time `json:"started_at"`
	LastActiveAt  time.Time `json:"last_active_at"`
}

type Command struct {
	ID           string           `json:"id"`
	SessionID    string           `json:"session_id"`
	RawInput     string           `json:"raw_input"`
	InputType    CommandInputType `json:"input_type"`
	Status       CommandStatus    `json:"status"`
	Output       string           `json:"output,omitempty"`
	ErrorCode    string           `json:"error_code,omitempty"`
	ErrorMessage string           `json:"error_message,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	CompletedAt  time.Time        `json:"completed_at,omitempty"`
}

func New(id, workspaceRoot string, now time.Time) Session {
	return Session{
		ID:            id,
		WorkspaceRoot: workspaceRoot,
		StartedAt:     now,
		LastActiveAt:  now,
	}
}

func (s Session) Touch(now time.Time) Session {
	s.LastActiveAt = now
	return s
}

func NewCommand(id, sessionID, rawInput string, inputType CommandInputType, now time.Time) Command {
	return Command{
		ID:        id,
		SessionID: sessionID,
		RawInput:  rawInput,
		InputType: inputType,
		Status:    CommandStatusAccepted,
		CreatedAt: now,
	}
}
