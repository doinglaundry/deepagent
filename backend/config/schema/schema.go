package schema

type ModelConfig struct {
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	BaseURL        string `json:"base_url,omitempty"`
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`

	// SupportsThinking declares whether the provider/model accepts the
	// extended-thinking toggle. Mirrors the deerflow ModelConfig field of the
	// same name; lead_agent.MakeLeadAgent uses it to decide whether to honor
	// RuntimeContext.ThinkingEnabled or silently downgrade.
	SupportsThinking bool `json:"supports_thinking,omitempty"`

	// ThinkingBudgetTokens sets the per-request "extended thinking" budget
	// for providers that honour it (Claude). When 0 and thinking is
	// enabled, the chat model factory uses a sensible default (4096).
	ThinkingBudgetTokens int `json:"thinking_budget_tokens,omitempty"`

	// SupportsVision declares whether the model can ingest image content.
	// The agent.middlewares.ViewImage middleware uses this to decide whether
	// to enable the vision routing path. Mirrors deerflow's same-named flag.
	SupportsVision bool `json:"supports_vision,omitempty"`
}

// AgentConfig describes a custom agent. Mirrors deerflow's
// AgentConfig dataclass: a top-level YAML record under either
// `agents:` (inline map) or `<root>/agents/<name>/config.yaml`
// (per-agent directory).
type AgentConfig struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Instruction  string `json:"instruction,omitempty"`
	MaxIteration int    `json:"max_iteration,omitempty"`

	// Model overrides the global default model when non-empty.
	Model string `json:"model,omitempty"`

	// ToolGroups restricts the active tool surface for this agent.
	// nil means "inherit the lead-agent default"; an explicit empty
	// slice means "no tools" (advanced use case).
	ToolGroups []string `json:"tool_groups,omitempty"`

	// Skills behaves like ToolGroups: nil = inherit, [] = strict empty,
	// non-empty = subset selection. Mirrors Python deerflow semantics
	// for the prompt's <available_skills> section.
	Skills []string `json:"skills,omitempty"`
}

// SkillsConfig drives the SKILL.md scanner used to populate the
// <available_skills> prompt section. Each path is expanded with ~ and
// scanned one level deep for "<name>/SKILL.md".
type SkillsConfig struct {
	Paths []string `json:"paths,omitempty"`
}

// DeferredToolEntry describes a tool that is registered but not loaded by
// default — the agent has to opt in through the deferred-tool prompt section.
type DeferredToolEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ToolSearchConfig gates the deferred-tool prompt section + the
// DeferredTools middleware that filters those tools out of the active set.
type ToolSearchConfig struct {
	Enabled  bool                `json:"enabled"`
	Deferred []DeferredToolEntry `json:"deferred,omitempty"`
}

// ACPAgentEntry captures the prompt-side metadata for an external ACP
// agent (codex, claude_code, ...). The Description is what the LLM sees
// when deciding whether to invoke the agent.
type ACPAgentEntry struct {
	Description string `json:"description,omitempty"`
}

// ACPConfig holds the registered ACP agents whose mere presence flips on
// the ACP prompt subsection.
type ACPConfig struct {
	Agents map[string]ACPAgentEntry `json:"agents,omitempty"`
}

type Config struct {
	RootDir       string `json:"root_dir"`
	StateDir      string `json:"state_dir"`
	SessionsDir   string `json:"sessions_dir"`
	MemoryDir     string `json:"memory_dir"`
	CheckpointDir string `json:"checkpoint_dir"`

	// AgentsDir is the base directory containing per-agent
	// subdirectories ("<AgentsDir>/<name>/config.yaml"). Phase 6
	// uses it as the on-disk source for LoadAgentConfigFromDir.
	// Defaults to "<RootDir>/agents" when empty.
	AgentsDir string `json:"agents_dir,omitempty"`

	DefaultModel string                  `json:"default_model"`
	Models       map[string]*ModelConfig `json:"models"`
	DefaultAgent string                 `json:"default_agent"`
	Agents       map[string]AgentConfig `json:"agents"`

	RuntimeModel   string `json:"runtime_model"`
	RuntimeTimeout int    `json:"runtime_timeout"`

	// Phase 5 (data sources): wire these from yaml/config.yaml so the
	// PromptDeps builder can populate the corresponding prompt sections.
	Skills     SkillsConfig     `json:"skills,omitempty"`
	ToolSearch ToolSearchConfig `json:"tool_search,omitempty"`
	ACP        ACPConfig        `json:"acp,omitempty"`
}
