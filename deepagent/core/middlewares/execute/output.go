package execute

// ExecCommandOutput is the uniform result returned by command execution and
// policy checks. Denied results are produced before starting a process.
type ExecCommandOutput struct {
	Command   string `json:"command"`
	WorkDir   string `json:"workdir"`
	Output    string `json:"output"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
	Denied    bool   `json:"denied"`
	Reason    string `json:"reason"`
}
