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

func SandboxWorkDir(sessionID string) string {
	return filepath.Join(SessionTreeDir(sessionID), "workspace")
}

func SandboxUploadsDir(sessionID string) string {
	return filepath.Join(SessionTreeDir(sessionID), "uploads")
}

func SandboxOutputsDir(sessionID string) string {
	return filepath.Join(SessionTreeDir(sessionID), "outputs")
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

func EnsureSessionDirs(sessionID string) error {
	for _, dir := range []string{
		SandboxWorkDir(sessionID),
		SandboxUploadsDir(sessionID),
		SandboxOutputsDir(sessionID),
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
