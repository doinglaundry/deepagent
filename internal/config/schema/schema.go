package schema

type Config struct {
	RootDir         string `json:"root_dir"`
	StateDir        string `json:"state_dir"`
	SessionsDir     string `json:"sessions_dir"`
	MemoryDir       string `json:"memory_dir"`
	CheckpointDir   string `json:"checkpoint_dir"`
	RuntimeProvider string `json:"runtime_provider"`
	RuntimeBaseURL  string `json:"runtime_base_url"`
	RuntimeModel    string `json:"runtime_model"`
	RuntimeTimeout  int    `json:"runtime_timeout"`
}
