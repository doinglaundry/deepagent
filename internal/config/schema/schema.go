package schema

type Config struct {
	RootDir      string `json:"root_dir"`
	StateDir     string `json:"state_dir"`
	SessionsDir  string `json:"sessions_dir"`
	MemoryDir    string `json:"memory_dir"`
	CheckpointDir string `json:"checkpoint_dir"`
}
