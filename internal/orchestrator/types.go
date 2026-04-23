package orchestrator

type Mode string

type CommandStatus string

type AgentRunStatus string

const (
	ModeSingleAgent Mode = "single_agent"

	CommandStatusAccepted  CommandStatus = "accepted"
	CommandStatusRunning   CommandStatus = "running"
	CommandStatusCompleted CommandStatus = "completed"
	CommandStatusFailed    CommandStatus = "failed"

	AgentRunStatusPending   AgentRunStatus = "pending"
	AgentRunStatusStreaming AgentRunStatus = "streaming"
	AgentRunStatusCompleted AgentRunStatus = "completed"
	AgentRunStatusFailed    AgentRunStatus = "failed"
)

type Command struct {
	ID        string        `json:"id"`
	SessionID string        `json:"session_id"`
	RawInput  string        `json:"raw_input"`
	Status    CommandStatus `json:"status"`
}
