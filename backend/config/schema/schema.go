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

	// SupportsVision declares whether the model can ingest image content.
	// The agent.middlewares.ViewImage middleware uses this to decide whether
	// to enable the vision routing path. Mirrors deerflow's same-named flag.
	SupportsVision bool `json:"supports_vision,omitempty"`
}

type AgentConfig struct {
	Name         string `json:"name"`
	Instruction  string `json:"instruction"`
	MaxIteration int    `json:"max_iteration"`
}

type Config struct {
	RootDir       string `json:"root_dir"`
	StateDir      string `json:"state_dir"`
	SessionsDir   string `json:"sessions_dir"`
	MemoryDir     string `json:"memory_dir"`
	CheckpointDir string `json:"checkpoint_dir"`

	DefaultModel string                  `json:"default_model"`
	Models       map[string]*ModelConfig `json:"models"`
	DefaultAgent string                 `json:"default_agent"`
	Agents       map[string]AgentConfig `json:"agents"`

	RuntimeModel   string `json:"runtime_model"`
	RuntimeTimeout int    `json:"runtime_timeout"`
}
