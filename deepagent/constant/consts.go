package consts

import "time"

const DefaultSessionID = "default_session_id"

type PermissionMode string

const (
	ModeDefault     PermissionMode = "default"
	ModeAcceptEdits PermissionMode = "acceptEdits"
	ModePlan        PermissionMode = "plan"
	ModeBypass      PermissionMode = "bypassPermissions"
)

const PlanModeDeniedMessage = "This action is blocked by plan mode. Switch to default or acceptEdits mode to perform writes."

const (
	TracePhaseBefore = iota + 1
	TracePhaseAfter
	TracePhaseTodos
	TracePhaseTokens
)

const AskClarificationToolName = "ask_clarification"

const (
	NoFilesFound   = "No files found"
	NoMatchesFound = "No matches found"
)

const HostBashDisabledMessage = "Host bash execution is disabled for LocalSandboxManager because it is not a secure sandbox boundary. Switch to AioSandboxManager (sandbox.use=aio) for isolated bash access, or set sandbox.allow_host_bash: true only in a fully trusted local environment."

const (
	FactKindEnduring = "enduring"
	FactKindEpisodic = "episodic"
)

const (
	DefaultSandboxImage           = "enterprise-public-cn-beijing.cr.volces.com/vefaas-public/all-in-one-sandbox:latest"
	DefaultSandboxContainerPrefix = "deer-flow-sandbox"
	DefaultSandboxIdleTimeout     = 10 * time.Minute
	DefaultSandboxReplicas        = 3
)

const (
	DefaultAgentKey         = "default"
	DefaultAgentInstruction = "You are a helpful assistant. You have access to filesystem tools (read files, list directories, search file contents, write and edit files) and a shell for running commands. Use these tools proactively to answer questions and complete tasks. For internet access, use the shell tool to run curl commands."
	DefaultAgentIterations  = 50
)

const (
	LocalSessionIDPrefix = "local:"
)
