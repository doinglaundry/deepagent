package sandboxpaths

import (
	"fmt"
	"path/filepath"
	"strings"
)

type ResolvedHostPath struct {
	HostPath string
	Mapping  *MountMapping
}

func ResolveHostPath(mappings []MountMapping, virtualPath string) (ResolvedHostPath, error) {
	m, rel := findVirtualPathMapping(mappings, virtualPath)
	if m == nil {
		return ResolvedHostPath{HostPath: virtualPath}, nil
	}
	hostRoot, err := filepath.Abs(m.HostPath)
	if err != nil {
		return ResolvedHostPath{}, err
	}
	joined := hostRoot
	if rel != "" {
		joined = filepath.Join(hostRoot, rel)
	}
	cleaned, err := filepath.Abs(joined)
	if err != nil {
		return ResolvedHostPath{}, err
	}
	if !isUnder(cleaned, hostRoot) {
		return ResolvedHostPath{}, fmt.Errorf("path escapes mount root: %s", virtualPath)
	}
	return ResolvedHostPath{HostPath: cleaned, Mapping: m}, nil
}

func findVirtualPathMapping(mappings []MountMapping, path string) (*MountMapping, string) {
	var best *MountMapping
	bestRel := ""
	bestLen := -1
	for i := range mappings {
		rel, ok := virtualPathRel(mappings[i].VirtualPath, path)
		if n := len(strings.TrimRight(mappings[i].VirtualPath, "/")); ok && n > bestLen {
			best, bestRel, bestLen = &mappings[i], rel, n
		}
	}
	return best, bestRel
}

func virtualPathRel(virtualPrefix, path string) (string, bool) {
	virtualPrefix = strings.TrimRight(virtualPrefix, "/")
	if virtualPrefix == "" {
		return strings.TrimPrefix(path, "/"), strings.HasPrefix(path, "/")
	}
	if path == virtualPrefix {
		return "", true
	}
	if strings.HasPrefix(path, virtualPrefix+"/") {
		return strings.TrimPrefix(path[len(virtualPrefix):], "/"), true
	}
	return "", false
}

func isUnder(child, parent string) bool {
	if child == parent {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, parent+sep)
}
