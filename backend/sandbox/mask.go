package sandbox

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"eino-cli/backend/sandboxpaths"
)

func MaskHostPathsInOutput(mappings []sandboxpaths.MountMapping, output string) string {
	if len(mappings) == 0 || output == "" {
		return output
	}
	result := output
	for _, m := range sortedMountMappings(mappings, func(a, b sandboxpaths.MountMapping) bool {
		return len(a.HostPath) > len(b.HostPath)
	}) {
		hostPath, err := filepath.Abs(m.HostPath)
		if err != nil {
			continue
		}
		pattern := regexp.QuoteMeta(hostPath) + `(?:[/\\][^\s"';&|<>()]*)?`
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		result = re.ReplaceAllStringFunc(result, func(match string) string {
			return ReverseResolvePath(mappings, match)
		})
	}
	return result
}

func ReverseResolvePath(mappings []sandboxpaths.MountMapping, path string) string {
	cleaned := absPath(filepath.FromSlash(path))
	m, rel := findHostPathMapping(mappings, cleaned)
	if m == nil {
		return cleaned
	}
	if rel == "" {
		return m.VirtualPath
	}
	return strings.TrimRight(m.VirtualPath, "/") + "/" + filepath.ToSlash(rel)
}

func findHostPathMapping(mappings []sandboxpaths.MountMapping, path string) (*sandboxpaths.MountMapping, string) {
	var best *sandboxpaths.MountMapping
	bestRel := ""
	bestLen := -1
	for i := range mappings {
		hostPath, err := filepath.Abs(mappings[i].HostPath)
		if err != nil {
			continue
		}
		if rel, ok := hostPathRel(hostPath, path); ok && len(hostPath) > bestLen {
			best, bestRel, bestLen = &mappings[i], rel, len(hostPath)
		}
	}
	return best, bestRel
}

func hostPathRel(hostPath, path string) (string, bool) {
	if path == hostPath {
		return "", true
	}
	sep := string(filepath.Separator)
	if strings.HasPrefix(path, hostPath+sep) {
		return strings.TrimPrefix(path[len(hostPath):], sep), true
	}
	return "", false
}

func absPath(path string) string {
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return cleaned
}

func sortedMountMappings(mappings []sandboxpaths.MountMapping, less func(a, b sandboxpaths.MountMapping) bool) []sandboxpaths.MountMapping {
	sorted := append([]sandboxpaths.MountMapping(nil), mappings...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })
	return sorted
}
