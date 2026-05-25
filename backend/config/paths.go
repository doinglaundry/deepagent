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

func SessionTreeDir(sessionID string) string {
	return filepath.Join(BaseDir(), "sessions", sessionID)
}

func SessionRunsDir(sessionID string) string {
	return filepath.Join(SessionTreeDir(sessionID), "runs")
}

func SessionRollbackDir(sessionID string) string {
	return filepath.Join(SessionTreeDir(sessionID), "rollback")
}

func SessionCheckpointsDir(sessionID string) string {
	return filepath.Join(SessionTreeDir(sessionID), "checkpoints")
}

func MemoryDir() string {
	return filepath.Join(BaseDir(), "memory")
}

func DreamMemoryDir() string {
	return filepath.Join(BaseDir(), "dream-memory")
}

func TranscriptDir() string {
	return filepath.Join(BaseDir(), "transcripts")
}

func AgentMessagesLogPath() string {
	return filepath.Join(BaseDir(), "agent-messages.md")
}

func UserDir(uid string) string {
	return filepath.Join(BaseDir(), "users", uid)
}

func SessionDir(sessionID, uid string) string {
	return filepath.Join(UserDir(uid), "sessions", sessionID)
}

func SandboxUserDataDir(sessionID, uid string) string {
	return filepath.Join(SessionDir(sessionID, uid), "user-data")
}

func SandboxWorkDir(sessionID, uid string) string {
	return filepath.Join(SandboxUserDataDir(sessionID, uid), "workspace")
}

func SandboxUploadsDir(sessionID, uid string) string {
	return filepath.Join(SandboxUserDataDir(sessionID, uid), "uploads")
}

func SandboxOutputsDir(sessionID, uid string) string {
	return filepath.Join(SandboxUserDataDir(sessionID, uid), "outputs")
}

func EnsureSessionDirs(sessionID, uid string) error {
	for _, dir := range []string{
		SandboxWorkDir(sessionID, uid),
		SandboxUploadsDir(sessionID, uid),
		SandboxOutputsDir(sessionID, uid),
		SessionRunsDir(sessionID),
		SessionRollbackDir(sessionID),
		SessionCheckpointsDir(sessionID),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
