package config

type ModelConfig struct {
	Name                 string `json:"name"`
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	BaseURL              string `json:"base_url,omitempty"`
	APIKey               string `json:"api_key,omitempty"`
	TimeoutSeconds       int    `json:"timeout_seconds,omitempty"`
	SupportsThinking     bool   `json:"supports_thinking,omitempty"`
	ThinkingBudgetTokens int    `json:"thinking_budget_tokens,omitempty"`
	SupportsVision       bool   `json:"supports_vision,omitempty"`
}

type AgentConfig struct {
	Name         string   `json:"name"                    yaml:"name"`
	Description  string   `json:"description,omitempty"   yaml:"description,omitempty"`
	Instruction  string   `json:"instruction,omitempty"   yaml:"instruction,omitempty"`
	MaxIteration int      `json:"max_iteration,omitempty" yaml:"max_iteration,omitempty"`
	Model        string   `json:"model,omitempty" yaml:"model,omitempty"`
	ToolGroups   []string `json:"tool_groups,omitempty" yaml:"tool_groups,omitempty"`
	Skills       []string `json:"skills,omitempty" yaml:"skills,omitempty"`
}

type SkillsConfig struct {
	Paths   []string        `json:"paths,omitempty"   yaml:"paths,omitempty"`
	Enabled map[string]bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}
type DeferredToolEntry struct {
	Name        string `json:"name"                  yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ToolSearchConfig gates the deferred-tool prompt section + the
// DeferredTools middleware that filters those tools out of the active set.
type ToolSearchConfig struct {
	Enabled  bool                `json:"enabled"            yaml:"enabled"`
	Deferred []DeferredToolEntry `json:"deferred,omitempty" yaml:"deferred,omitempty"`
}

// ACPAgentEntry captures the prompt-side metadata for an external ACP
// agent (codex, claude_code, ...). The Description is what the LLM sees
// when deciding whether to invoke the agent.
type ACPAgentEntry struct {
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ACPConfig holds the registered ACP agents whose mere presence flips on
// the ACP prompt subsection.
type ACPConfig struct {
	Agents map[string]ACPAgentEntry `json:"agents,omitempty" yaml:"agents,omitempty"`
}

// Config is the application's single source of truth. It carries
// two layers in one struct:
//
//   - Runtime fields populated by Load() from os.Getwd / env vars /
//     built-in defaults. Tagged yaml:"-" so yaml.Unmarshal skips
//     them and they survive a YAML decode.
//
//   - YAML fields populated by Config.UnmarshalYAML (defined in
//     yaml.go) directly from yaml/config.yaml. Tags mirror the
//     file's top-level keys one-for-one, so a typo on either side
//     is immediately visible against the canonical declarations
//     here.
//
// This replaces an older "Config + FileConfig" split. The split
// existed because YAML's `models:` is a list while runtime needs
// map[string]*ModelConfig. UnmarshalYAML now performs that
// translation in-place via the alias trick (see yaml.go), so
// downstream packages see exactly one config type.
//
// New top-level YAML sections only need to be declared here once
// — UnmarshalYAML's alias handles them automatically. Sections
// without a runtime consumer carry json:"-" so they don't leak
// into JSON dumps.
type Config struct {
	RootDir        string                 `json:"root_dir"        yaml:"-"`
	PersistenceDir string                 `json:"persistence_dir" yaml:"-"`
	SessionsDir    string                 `json:"sessions_dir"    yaml:"-"`
	MemoryDir      string                 `json:"memory_dir"      yaml:"-"`
	CheckpointDir  string                 `json:"checkpoint_dir"  yaml:"-"`
	DefaultAgent   string                 `json:"default_agent"   yaml:"-"`
	Agents         map[string]AgentConfig `json:"agents"          yaml:"-"`
	ACP            ACPConfig              `json:"acp,omitempty"   yaml:"-"`

	// Fields below this line are sourced from yaml/config.yaml. The
	// order matches the file's top-level sections so a side-by-side
	// read remains easy.

	DefaultModel   string                  `json:"default_model"           yaml:"default_model"`
	ConfigVersion  int                     `json:"-"                       yaml:"config_version"`
	LogLevel       string                  `json:"-"                       yaml:"log_level"`
	TokenUsage     TokenUsage              `json:"-"                       yaml:"token_usage"`
	Models         map[string]*ModelConfig `json:"models"                  yaml:"-"` // built from the YAML list via UnmarshalYAML + normalizeModels
	ToolGroups     []ToolGroup             `json:"-"                       yaml:"tool_groups"`
	Tools          []Tool                  `json:"-"                       yaml:"tools"`
	ToolSearch     ToolSearchConfig        `json:"tool_search,omitempty"   yaml:"tool_search"`
	Uploads        Uploads                 `json:"-"                       yaml:"uploads"`
	Sandbox        Sandbox                 `json:"-"                       yaml:"sandbox"`
	Skills         SkillsConfig            `json:"skills,omitempty"        yaml:"skills"`
	Title          Title                   `json:"-"                       yaml:"title"`
	Summarization  Summarization           `json:"-"                       yaml:"summarization"`
	Memory         Memory                  `json:"-"                       yaml:"memory"`
	AgentsAPI      AgentsAPI               `json:"-"                       yaml:"agents_api"`
	SkillEvolution SkillEvolution          `json:"-"                       yaml:"skill_evolution"`
	CheckPointer   Checkpointer            `json:"-" yaml:"checkpointer"`
}
