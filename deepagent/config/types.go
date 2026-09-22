package config

import "time"

// VolumeMount declares a host-to-container path binding.
type VolumeMount struct {
	HostPath      string `yaml:"host_path"`
	ContainerPath string `yaml:"container_path"`
	ReadOnly      bool   `yaml:"read_only,omitempty"`
}

// SandboxConfig selects and tunes the per-session sandbox manager (use=local|aio).
type SandboxConfig struct {
	Use                    string            `yaml:"use"`
	AllowHostBash          bool              `yaml:"allow_host_bash"`
	Image                  string            `yaml:"image"`
	ContainerPrefix        string            `yaml:"container_prefix"`
	Port                   int               `yaml:"port"`
	Replicas               int               `yaml:"replicas"`
	IdleTimeout            time.Duration     `yaml:"idle_timeout"`
	Mounts                 []VolumeMount     `yaml:"mounts,omitempty"`
	Environment            map[string]string `yaml:"environment,omitempty"`
	BashOutputMaxChars     int               `yaml:"bash_output_max_chars"`
	ReadFileOutputMaxChars int               `yaml:"read_file_output_max_chars"`
	LsOutputMaxChars       int               `yaml:"ls_output_max_chars"`
}

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

// Config carries external model, web search, sandbox, and logging configuration.
type Config struct {
	DefaultModel string                  `json:"default_model" yaml:"default_model"`
	LogLevel     string                  `json:"-"             yaml:"log_level"`
	Models       map[string]*ModelConfig `json:"models"        yaml:"-"`
	WebSearch    WebSearch               `json:"-"             yaml:"web_search"`
	Sandbox      SandboxConfig           `json:"-"             yaml:"sandbox"`
}
