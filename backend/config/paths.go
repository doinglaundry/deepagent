package config

import (
	"os"
	"path/filepath"
)

func BaseDir(cfg *Config) string {
	return filepath.Join(cfg.RootDir, ".eino-cli")
}

func CheckpointsDir(cfg *Config) string {
	return filepath.Join(BaseDir(cfg), "checkpoints")
}

func RunsDir(cfg *Config) string {
	return filepath.Join(BaseDir(cfg), "runs")
}

func RollbackDir(cfg *Config) string {
	return filepath.Join(BaseDir(cfg), "rollback")
}

func RollbackRunDir(cfg *Config, runID string) string {
	return filepath.Join(RollbackDir(cfg), runID)
}

func MemoryDir(cfg *Config) string {
	return filepath.Join(BaseDir(cfg), "memory")
}

func InputHistoryPath(cfg *Config) string {
	return filepath.Join(BaseDir(cfg), "history.txt")
}

func LogPath(cfg *Config) string {
	return filepath.Join(BaseDir(cfg), "eino-cli.log")
}

func AgentMessagesLogPath(cfg *Config) string {
	return filepath.Join(BaseDir(cfg), "agent-messages.log")
}

func SkillDir(cfg *Config, name string) string {
	return filepath.Join(BaseDir(cfg), name)
}

// UserDir returns the on-disk directory for uid.
func UserDir(cfg *Config, uid string) string {
	return filepath.Join(BaseDir(cfg), "users", uid)
}

// ThreadDir returns the on-disk directory for (tid, uid).
func ThreadDir(cfg *Config, tid, uid string) string {
	return filepath.Join(UserDir(cfg, uid), "threads", tid)
}

// SandboxUserDataDir is the host side of /mnt/user-data.
func SandboxUserDataDir(cfg *Config, tid, uid string) string {
	return filepath.Join(ThreadDir(cfg, tid, uid), "user-data")
}

// SandboxWorkDir is the host side of /mnt/user-data/workspace.
func SandboxWorkDir(cfg *Config, tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(cfg, tid, uid), "workspace")
}

// SandboxUploadsDir is the host side of /mnt/user-data/uploads.
func SandboxUploadsDir(cfg *Config, tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(cfg, tid, uid), "uploads")
}

// SandboxOutputsDir is the host side of /mnt/user-data/outputs.
func SandboxOutputsDir(cfg *Config, tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(cfg, tid, uid), "outputs")
}

// EnsureThreadDirs creates every per-thread directory tools may write into.
func EnsureThreadDirs(cfg *Config, tid, uid string) error {
	for _, dir := range []string{
		SandboxWorkDir(cfg, tid, uid),
		SandboxUploadsDir(cfg, tid, uid),
		SandboxOutputsDir(cfg, tid, uid),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
