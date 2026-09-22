package sandboxpaths

import (
	"fmt"
	"path/filepath"
	"strings"
)

func GetHostPath(mappings []MountMapping, virtualPath string) (string, error) {
	mount, relativePath := findMountForVirtualPath(mappings, virtualPath)
	if mount == nil {
		return virtualPath, nil
	}
	hostRoot, err := filepath.Abs(mount.HostPath)
	if err != nil {
		return "", err
	}
	hostPath := hostRoot
	if relativePath != "" {
		hostPath = filepath.Join(hostRoot, relativePath)
	}
	cleanedHostPath, err := filepath.Abs(hostPath)
	if err != nil {
		return "", err
	}
	if !isUnder(cleanedHostPath, hostRoot) {
		return "", fmt.Errorf("path escapes mount root: %s", virtualPath)
	}
	return cleanedHostPath, nil
}

func findMountForVirtualPath(mappings []MountMapping, virtualPath string) (*MountMapping, string) {
	var bestMount *MountMapping
	bestRelativePath := ""
	bestLen := -1
	for i := range mappings {
		relativePath, ok := getPathRelativeToVirtualRoot(mappings[i].VirtualPath, virtualPath)
		virtualRootLen := len(strings.TrimRight(mappings[i].VirtualPath, "/"))
		if ok && virtualRootLen > bestLen {
			bestMount = &mappings[i]
			bestRelativePath = relativePath
			bestLen = virtualRootLen
		}
	}
	return bestMount, bestRelativePath
}

func getPathRelativeToVirtualRoot(virtualRoot, virtualPath string) (string, bool) {
	virtualRoot = strings.TrimRight(virtualRoot, "/")
	if virtualRoot == "" {
		return strings.TrimPrefix(virtualPath, "/"), strings.HasPrefix(virtualPath, "/")
	}
	if virtualPath == virtualRoot {
		return "", true
	}
	if strings.HasPrefix(virtualPath, virtualRoot+"/") {
		return strings.TrimPrefix(virtualPath[len(virtualRoot):], "/"), true
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
