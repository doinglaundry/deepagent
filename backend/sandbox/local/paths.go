package local

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"eino-cli/backend/sandbox"
)

// PathMapping binds a container path prefix to a host directory.
type PathMapping struct {
	ContainerPath string
	LocalPath     string
	ReadOnly      bool
}

// resolved records the rewrite result plus which mapping fired (nil for passthrough).
type resolved struct {
	Path    string
	Mapping *PathMapping
}

// findPathMapping returns the longest-prefix mapping for path; nil on no match.
func findPathMapping(mappings []PathMapping, path string) (*PathMapping, string) {
	var best *PathMapping
	bestRel := ""
	bestLen := -1
	for i := range mappings {
		rel, ok := containerRel(mappings[i].ContainerPath, path)
		if n := len(strings.TrimRight(mappings[i].ContainerPath, "/")); ok && n > bestLen {
			best, bestRel, bestLen = &mappings[i], rel, n
		}
	}
	return best, bestRel
}

func containerRel(container, path string) (string, bool) {
	container = strings.TrimRight(container, "/")
	if container == "" {
		return strings.TrimPrefix(path, "/"), strings.HasPrefix(path, "/")
	}
	if path == container {
		return "", true
	}
	if strings.HasPrefix(path, container+"/") {
		return strings.TrimPrefix(path[len(container):], "/"), true
	}
	return "", false
}

// resolvePath maps a container path to its host path; rejects `..` escapes.
func resolvePath(mappings []PathMapping, path string) (resolved, error) {
	m, rel := findPathMapping(mappings, path)
	if m == nil {
		return resolved{Path: path}, nil
	}
	localRoot, err := filepath.Abs(m.LocalPath)
	if err != nil {
		return resolved{}, err
	}
	joined := localRoot
	if rel != "" {
		joined = filepath.Join(localRoot, rel)
	}
	// Path-escape guard: Clean+Abs collapses `..`, re-check cleaned is under localRoot.
	cleaned, err := filepath.Abs(joined)
	if err != nil {
		return resolved{}, err
	}
	if !isUnder(cleaned, localRoot) {
		return resolved{}, sandbox.NewPermissionError("path escapes mount root", path)
	}
	return resolved{Path: cleaned, Mapping: m}, nil
}

func isUnder(child, parent string) bool {
	if child == parent {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, parent+sep)
}

// reverseResolvePath maps a host path back to its container path; longest mapping wins.
func reverseResolvePath(mappings []PathMapping, path string) string {
	cleaned := absPath(filepath.FromSlash(path))
	m, rel := findLocalPathMapping(mappings, cleaned)
	if m == nil {
		return cleaned
	}
	if rel == "" {
		return m.ContainerPath
	}
	return strings.TrimRight(m.ContainerPath, "/") + "/" + filepath.ToSlash(rel)
}

// reverseResolvePathsInOutput rewrites host paths back to container paths in arbitrary text.
func reverseResolvePathsInOutput(mappings []PathMapping, output string) string {
	if len(mappings) == 0 || output == "" {
		return output
	}
	result := output
	for _, m := range sortedMappings(mappings, func(a, b PathMapping) bool {
		return len(a.LocalPath) > len(b.LocalPath)
	}) {
		local, err := filepath.Abs(m.LocalPath)
		if err != nil {
			continue
		}
		pattern := regexp.QuoteMeta(local) + `(?:[/\\][^\s"';&|<>()]*)?`
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		result = re.ReplaceAllStringFunc(result, func(match string) string {
			return reverseResolvePath(mappings, match)
		})
	}
	return result
}

// resolvePathsInCommand rewrites every /mnt/* in a shell command to its host path.
func resolvePathsInCommand(mappings []PathMapping, command string) string {
	return rewriteContainerPaths(mappings, command, `(?:/|$|[\s"';&|<>()])`, false)
}

// resolvePathsInContent rewrites /mnt/* in file content; forward slashes only.
func resolvePathsInContent(mappings []PathMapping, content string) string {
	return rewriteContainerPaths(mappings, content, `(?:/|$|[^\w./-])`, true)
}

func rewriteContainerPaths(mappings []PathMapping, src, boundary string, forwardSlash bool) string {
	if len(mappings) == 0 || src == "" {
		return src
	}
	sorted := sortedMappings(mappings, func(a, b PathMapping) bool {
		return len(a.ContainerPath) > len(b.ContainerPath)
	})

	var alts []string
	for _, m := range sorted {
		alts = append(alts, "("+regexp.QuoteMeta(m.ContainerPath)+boundary+`(?:/[^\s"';&|<>()]*)?`+")")
	}
	if len(alts) == 0 {
		return src
	}
	re, err := regexp.Compile(strings.Join(alts, "|"))
	if err != nil {
		return src
	}
	return re.ReplaceAllStringFunc(src, func(match string) string {
		core := match
		for _, m := range sorted {
			if strings.HasPrefix(core, m.ContainerPath) {
				rest := core[len(m.ContainerPath):]
				if rest == "" {
					return m.LocalPath
				}
				if rest[0] == '/' {
					r, err := resolvePath(mappings, core)
					if err != nil {
						return match
					}
					out := r.Path
					if forwardSlash {
						out = filepath.ToSlash(out)
					}
					return out
				}
				return m.LocalPath + rest
			}
		}
		return match
	})
}

// isReadOnlyPath returns the ReadOnly flag of the longest mapping containing path.
func isReadOnlyPath(mappings []PathMapping, resolvedPath string) bool {
	m, _ := findLocalPathMapping(mappings, absPath(resolvedPath))
	return m != nil && m.ReadOnly
}

func findLocalPathMapping(mappings []PathMapping, path string) (*PathMapping, string) {
	var best *PathMapping
	bestRel := ""
	bestLen := -1
	for i := range mappings {
		local, err := filepath.Abs(mappings[i].LocalPath)
		if err != nil {
			continue
		}
		if rel, ok := localRel(local, path); ok && len(local) > bestLen {
			best, bestRel, bestLen = &mappings[i], rel, len(local)
		}
	}
	return best, bestRel
}

func localRel(local, path string) (string, bool) {
	if path == local {
		return "", true
	}
	if strings.HasPrefix(path, local+string(filepath.Separator)) {
		return strings.TrimPrefix(path[len(local):], string(filepath.Separator)), true
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

func sortedMappings(mappings []PathMapping, less func(a, b PathMapping) bool) []PathMapping {
	sorted := append([]PathMapping(nil), mappings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return less(sorted[i], sorted[j])
	})
	return sorted
}
