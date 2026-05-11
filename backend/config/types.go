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
	ReasoningEffort      string `json:"reasoning_effort,omitempty"`
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

// ToolSearchConfig gates the deferred-tool prompt section and middleware filter.
type ToolSearchConfig struct {
	Enabled  bool                `json:"enabled"            yaml:"enabled"`
	Deferred []DeferredToolEntry `json:"deferred,omitempty" yaml:"deferred,omitempty"`
}

// ACPAgentEntry: prompt-side description shown to the LLM for an external agent.
type ACPAgentEntry struct {
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ACPConfig: registered ACP agents; presence flips on the ACP prompt subsection.
type ACPConfig struct {
	Agents map[string]ACPAgentEntry `json:"agents,omitempty" yaml:"agents,omitempty"`
}

// Config is the single source of truth: yaml-tagged fields decode from
// yaml/config.yaml; yaml:"-" fields are filled by Load() at runtime.
type Config struct {
	RootDir        string                 `json:"root_dir"        yaml:"-"`
	DefaultAgent   string                 `json:"default_agent"   yaml:"-"`
	Agents         map[string]AgentConfig `json:"agents"          yaml:"-"`
	ACP            ACPConfig              `json:"acp,omitempty"   yaml:"-"`

	// Fields below mirror yaml/config.yaml's top-level sections in order.

	DefaultModel   string                  `json:"default_model"           yaml:"default_model"`
	ConfigVersion  int                     `json:"-"                       yaml:"config_version"`
	LogLevel       string                  `json:"-"                       yaml:"log_level"`
	TokenUsage     TokenUsage              `json:"-"                       yaml:"token_usage"`
	Models         map[string]*ModelConfig `json:"models"                  yaml:"-"` // built from the YAML list via UnmarshalYAML + normalizeModels
	ToolGroups     []ToolGroup             `json:"-"                       yaml:"tool_groups"`
	Tools          []Tool                  `json:"-"                       yaml:"tools"`
	ToolSearch     ToolSearchConfig        `json:"tool_search,omitempty"   yaml:"tool_search"`
	Uploads        Uploads                 `json:"-"                       yaml:"uploads"`
	Skills         SkillsConfig            `json:"skills,omitempty"        yaml:"skills"`
	Title          Title                   `json:"-"                       yaml:"title"`
	Summarization  Summarization           `json:"-"                       yaml:"summarization"`
	Memory         Memory                  `json:"-"                       yaml:"memory"`
	ErrorHandling  ErrorHandling           `json:"-"                       yaml:"error_handling"`
	AgentsAPI      AgentsAPI               `json:"-"                       yaml:"agents_api"`
	SkillEvolution SkillEvolution          `json:"-"                       yaml:"skill_evolution"`
	CheckPointer   Checkpointer            `json:"-" yaml:"checkpointer"`
}
