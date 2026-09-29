package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var rootDirOverride string

var safeSessionID = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

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

func ValidateSessionID(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("invalid session_id: empty")
	}
	if sessionID == "." || sessionID == ".." {
		return fmt.Errorf("invalid session_id: %q", sessionID)
	}
	if !safeSessionID.MatchString(sessionID) {
		return fmt.Errorf("invalid session_id: %q", sessionID)
	}
	return nil
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
	sessionRoot, err := openSessionDir(sessionID, true)
	if err != nil {
		return err
	}
	defer sessionRoot.Close()

	for _, dir := range []string{"workspace", "uploads", "outputs", "runs", "rollback", "checkpoints"} {
		err = ensureDirectory(sessionRoot, dir)
		if err != nil {
			return err
		}
	}
	return nil
}

func OpenSessionDir(sessionID string) (*os.Root, error) {
	return openSessionDir(sessionID, false)
}

func openSessionDir(sessionID string, create bool) (*os.Root, error) {
	err := ValidateSessionID(sessionID)
	if err != nil {
		return nil, err
	}
	sessionsRoot, err := openSessionsRoot(create)
	if err != nil {
		return nil, err
	}
	sessionRoot, err := openChildRoot(sessionsRoot, sessionID, create)
	_ = sessionsRoot.Close()
	if err != nil {
		return nil, err
	}
	return sessionRoot, nil
}

func OpenRootDir() (*os.Root, error) {
	rootPath, err := filepath.Abs(RootDir())
	if err != nil {
		return nil, err
	}
	expected, err := os.Lstat(rootPath)
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", rootPath)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, opened) {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("root changed while opening: %s", rootPath)
	}
	return root, nil
}

func openSessionsRoot(create bool) (*os.Root, error) {
	root, err := OpenRootDir()
	if err != nil {
		return nil, err
	}
	base, err := openChildRoot(root, ".eino-cli", create)
	_ = root.Close()
	if err != nil {
		return nil, err
	}
	sessions, err := openChildRoot(base, "sessions", create)
	_ = base.Close()
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func openChildRoot(parent *os.Root, name string, create bool) (*os.Root, error) {
	if create {
		err := parent.Mkdir(name, 0o755)
		if err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing symlinked session directory %q", name)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session path %q is not a directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := child.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = child.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("session directory changed while opening %q", name)
	}
	return child, nil
}

func ensureDirectory(root *os.Root, name string) error {
	err := root.Mkdir(name, 0o755)
	if err != nil && !os.IsExist(err) {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlinked session directory %q", name)
	}
	if !info.IsDir() {
		return fmt.Errorf("session path %q is not a directory", name)
	}
	return nil
}
