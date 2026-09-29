package local

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"eino-cli/deepagent/sandbox"
	"eino-cli/deepagent/sandbox/paths"
)

type virtualPathTextKind int

const (
	shellCommandText virtualPathTextKind = iota
	fileContentText
)

func getHostPath(mappings []sandboxpaths.MountMapping, virtualPath string) (string, error) {
	resolved, err := resolveSandboxPath(mappings, virtualPath)
	if err != nil {
		return "", err
	}
	return resolved.HostPath, nil
}

func resolveSandboxPath(mappings []sandboxpaths.MountMapping, virtualPath string) (sandboxpaths.ResolvedPath, error) {
	resolved, err := sandboxpaths.ResolvePath(mappings, virtualPath)
	if err != nil {
		if strings.Contains(err.Error(), "path escapes mount root") {
			return sandboxpaths.ResolvedPath{}, sandbox.NewPermissionError("path escapes mount root", virtualPath)
		}
		return sandboxpaths.ResolvedPath{}, err
	}
	return resolved, nil
}

func replaceVirtualPathsWithHostPaths(mappings []sandboxpaths.MountMapping, text string, textKind virtualPathTextKind) string {
	if len(mappings) == 0 || text == "" {
		return text
	}
	boundaryPattern := `(?:/|$|[\s"';&|<>()])`
	useForwardSlashes := false
	if textKind == fileContentText {
		boundaryPattern = `(?:/|$|[^\w./-])`
		useForwardSlashes = true
	}

	sortedMappings := append([]sandboxpaths.MountMapping(nil), mappings...)
	sort.SliceStable(sortedMappings, func(i, j int) bool {
		return len(sortedMappings[i].VirtualPath) > len(sortedMappings[j].VirtualPath)
	})

	var virtualPathPatterns []string
	for _, mapping := range sortedMappings {
		virtualPathPatterns = append(virtualPathPatterns, "("+regexp.QuoteMeta(mapping.VirtualPath)+boundaryPattern+`(?:/[^\s"';&|<>()]*)?`+")")
	}
	if len(virtualPathPatterns) == 0 {
		return text
	}
	virtualPathPattern, err := regexp.Compile(strings.Join(virtualPathPatterns, "|"))
	if err != nil {
		return text
	}
	return virtualPathPattern.ReplaceAllStringFunc(text, func(match string) string {
		for _, mapping := range sortedMappings {
			if strings.HasPrefix(match, mapping.VirtualPath) {
				rest := match[len(mapping.VirtualPath):]
				if rest == "" {
					return mapping.HostPath
				}
				if rest[0] == '/' {
					hostPath, err := getHostPath(mappings, match)
					if err != nil {
						return match
					}
					if useForwardSlashes {
						hostPath = filepath.ToSlash(hostPath)
					}
					return hostPath
				}
				return mapping.HostPath + rest
			}
		}
		return match
	})
}

func isReadOnlyPath(mappings []sandboxpaths.MountMapping, hostPath string) bool {
	cleanedHostPath := canonicalPath(hostPath)

	readOnly := false
	bestLen := -1
	for i := range mappings {
		hostRoot := canonicalPath(mappings[i].HostPath)
		if !isUnder(cleanedHostPath, hostRoot) {
			continue
		}
		if len(hostRoot) > bestLen || (len(hostRoot) == bestLen && mappings[i].ReadOnly) {
			readOnly = mappings[i].ReadOnly
			bestLen = len(hostRoot)
		}
	}
	return readOnly
}

func canonicalPath(path string) string {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	current := absPath
	var suffix []string
	for {
		_, err = os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return absPath
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved
		}
		if !os.IsNotExist(err) {
			return absPath
		}
		parent := filepath.Dir(current)
		if parent == current {
			return absPath
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func isUnder(child, parent string) bool {
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}
