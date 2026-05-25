package sandboxpaths

import (
	"os"
	"path/filepath"

	"eino-cli/backend/config"
)

const (
	VirtualPathPrefixRepo      = "/mnt/repo"
	VirtualPathPrefixWorkspace = "/mnt/workspace"
	VirtualPathPrefixUploads   = "/mnt/uploads"
	VirtualPathPrefixOutputs   = "/mnt/outputs"
	VirtualPathPrefixSkills    = "/mnt/skills"
)

type MountMapping struct {
	VirtualPath string
	HostPath    string
	ReadOnly    bool
}

func BuildMountMappings(sessionID string) ([]MountMapping, error) {
	if err := config.EnsureSessionDirs(sessionID); err != nil {
		return nil, err
	}
	prefixToHostPath := map[string]string{
		VirtualPathPrefixRepo:      config.RootDir(),
		VirtualPathPrefixWorkspace: config.SandboxWorkDir(sessionID),
		VirtualPathPrefixUploads:   config.SandboxUploadsDir(sessionID),
		VirtualPathPrefixOutputs:   config.SandboxOutputsDir(sessionID),
	}
	out := []MountMapping{}
	if skillsHostPath := GetSkillsHostPath(); skillsHostPath != "" {
		out = append(out, MountMapping{
			VirtualPath: VirtualPathPrefixSkills,
			HostPath:    skillsHostPath,
			ReadOnly:    true,
		})
	}
	for virtualPathPrefix, hostPath := range prefixToHostPath {
		out = append(out, MountMapping{
			VirtualPath: virtualPathPrefix,
			HostPath:    hostPath,
			ReadOnly:    false,
		})
	}
	return out, nil
}

func GetSkillsHostPath() string {
	skillsRoot := filepath.Join(config.RootDir(), "backend", "skills")
	info, err := os.Stat(skillsRoot)
	if err != nil || !info.IsDir() {
		return ""
	}
	hostPath, err := filepath.Abs(skillsRoot)
	if err != nil {
		return ""
	}
	return hostPath
}
