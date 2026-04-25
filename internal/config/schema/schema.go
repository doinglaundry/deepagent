package schema

type ModelConfig struct {
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	BaseURL        string `json:"base_url,omitempty"`
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type AgentConfig struct {
	Name            string `json:"name"`
	Instruction     string `json:"instruction"`
	MaxIteration    int    `json:"max_iteration"`
	MaxHistoryTurns int    `yaml:"max_history_turns" json:"max_history_turns"`
}

type PluginGatewayConfig struct {
	Enabled        bool   `json:"enabled"`
	Endpoint       string `json:"endpoint,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type ProtocolConfig struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Config struct {
	RootDir       string `json:"root_dir"`
	StateDir      string `json:"state_dir"`
	SessionsDir   string `json:"sessions_dir"`
	MemoryDir     string `json:"memory_dir"`
	CheckpointDir string `json:"checkpoint_dir"`

	DefaultModel string                 `json:"default_model"`
	Models       map[string]ModelConfig `json:"models"`
	DefaultAgent string                 `json:"default_agent"`
	Agents       map[string]AgentConfig `json:"agents"`

	PluginGateway PluginGatewayConfig `json:"plugin_gateway"`
	Protocol      ProtocolConfig      `json:"protocol"`

	RuntimeBaseURL string `json:"runtime_base_url"`
	RuntimeModel   string `json:"runtime_model"`
	RuntimeTimeout int    `json:"runtime_timeout"`
}
