package config

import (
	"os"
	"path/filepath"
)

var rootDirOverride string

func RootDir() string {
	if rootDirOverride != "" {
		return rootDirOverride
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func SetRootDirForTest(root string) func() {
	previous := rootDirOverride
	rootDirOverride = root
	return func() {
		rootDirOverride = previous
	}
}

func BaseDir() string {
	return filepath.Join(RootDir(), ".eino-cli")
}

func CheckpointsDir() string {
	return filepath.Join(BaseDir(), "checkpoints")
}

func RunsDir() string {
	return filepath.Join(BaseDir(), "runs")
}

func RollbackDir() string {
	return filepath.Join(BaseDir(), "rollback")
}

func MemoryDir() string {
	return filepath.Join(BaseDir(), "memory")
}

func InputHistoryPath() string {
	return filepath.Join(BaseDir(), "history.txt")
}

func LogPath() string {
	return filepath.Join(BaseDir(), "eino-cli.log")
}

func AgentMessagesLogPath() string {
	return filepath.Join(BaseDir(), "agent-messages.md")
}

func UserDir(uid string) string {
	return filepath.Join(BaseDir(), "users", uid)
}

func ThreadDir(tid, uid string) string {
	return filepath.Join(UserDir(uid), "threads", tid)
}

func SandboxUserDataDir(tid, uid string) string {
	return filepath.Join(ThreadDir(tid, uid), "user-data")
}

func SandboxWorkDir(tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(tid, uid), "workspace")
}

func SandboxUploadsDir(tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(tid, uid), "uploads")
}

func SandboxOutputsDir(tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(tid, uid), "outputs")
}

func EnsureThreadDirs(tid, uid string) error {
	for _, dir := range []string{
		SandboxWorkDir(tid, uid),
		SandboxUploadsDir(tid, uid),
		SandboxOutputsDir(tid, uid),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
