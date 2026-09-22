package execute

// CommandCommit is the program-owned command prepared for policy checking
// and execution after model input has been normalized.
type CommandCommit struct {
	Args          []string          `json:"args"`
	Display       string            `json:"display"`
	RawCommand    string            `json:"raw_command"`
	WorkDir       string            `json:"workdir"`
	Timeout       int               `json:"timeout"`
	Env           map[string]string `json:"env"`
	Justification string            `json:"justification"`
}

type Action string

const (
	ActionAllow   Action = "allow"
	ActionApprove Action = "approve"
	ActionDeny    Action = "deny"
)

type Decision struct {
	Action     Action `json:"action"`
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason"`
	ApprovalID string `json:"approval_id,omitempty"`
}

// Risk levels are ordered from least to most restrictive by severity.
const (
	ClassificationModerate Classification = "moderate"
)

type SimpleCommand struct {
	Args       []string `json:"args"`
	Start      int      `json:"start"`
	End        int      `json:"end"`
	RawCommand string   `json:"raw_command"`
}

type CommandClassification struct {
	Classification Classification  `json:"classification"`
	Reason         string          `json:"reason"`
	FirstProgram   string          `json:"first_program"`
	Commands       []SimpleCommand `json:"commands"`
}
