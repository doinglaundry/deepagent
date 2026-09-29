package sandboxpaths

import (
	"os"
	"path/filepath"

	"eino-cli/deepagent/config"
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

type ResolvedPath struct {
	HostPath     string
	RelativePath string
	Mount        MountMapping
	Mapped       bool
}

func BuildMountMappings(sessionID string) ([]MountMapping, error) {
	err := config.ValidateSessionID(sessionID)
	if err != nil {
		return nil, err
	}
	err = config.EnsureSessionDirs(sessionID)
	if err != nil {
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
	root, err := config.OpenRootDir()
	if err != nil {
		return ""
	}
	defer root.Close()
	relative := filepath.Join("backend", "skills")
	info, err := root.Lstat(relative)
	if err != nil || !info.IsDir() {
		return ""
	}
	skills, err := root.OpenRoot(relative)
	if err != nil {
		return ""
	}
	defer skills.Close()
	opened, err := skills.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return ""
	}
	return skills.Name()
}
