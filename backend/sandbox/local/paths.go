package local

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandboxpaths"
)

type virtualPathTextKind int

const (
	shellCommandText virtualPathTextKind = iota
	fileContentText
)

func getHostPath(mappings []sandboxpaths.MountMapping, virtualPath string) (string, error) {
	hostPath, err := sandboxpaths.GetHostPath(mappings, virtualPath)
	if err != nil {
		if strings.Contains(err.Error(), "path escapes mount root") {
			return "", sandbox.NewPermissionError("path escapes mount root", virtualPath)
		}
		return "", err
	}
	return hostPath, nil
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
	cleanedHostPath, err := filepath.Abs(hostPath)
	if err != nil {
		cleanedHostPath = hostPath
	}

	readOnly := false
	bestLen := -1
	sep := string(filepath.Separator)
	for i := range mappings {
		hostRoot, err := filepath.Abs(mappings[i].HostPath)
		if err != nil {
			continue
		}
		if cleanedHostPath != hostRoot && !strings.HasPrefix(cleanedHostPath, hostRoot+sep) {
			continue
		}
		if len(hostRoot) > bestLen {
			readOnly = mappings[i].ReadOnly
			bestLen = len(hostRoot)
		}
	}
	return readOnly
}

func isUnder(child, parent string) bool {
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}
