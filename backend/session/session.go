package session

type CommandInputType string

const (
	CommandInputTypeNaturalLanguage CommandInputType = "natural_language"
	CommandInputTypeSlashCommand    CommandInputType = "slash_command"
)

type Session struct {
	ID            string `json:"id"`
	WorkspaceRoot string `json:"workspace_root"`
}

func New(id, workspaceRoot string) Session {
	return Session{
		ID:            id,
		WorkspaceRoot: workspaceRoot,
	}
}
