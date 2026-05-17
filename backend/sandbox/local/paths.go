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
	sorted := make([]PathMapping, len(mappings))
	copy(sorted, mappings)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(strings.TrimRight(sorted[i].ContainerPath, "/")) >
			len(strings.TrimRight(sorted[j].ContainerPath, "/"))
	})

	for i := range sorted {
		m := &sorted[i]
		container := strings.TrimRight(m.ContainerPath, "/")
		if container == "" {
			if strings.HasPrefix(path, "/") {
				return &mappings[indexOf(mappings, *m)], strings.TrimPrefix(path, "/")
			}
			continue
		}
		if path == container || strings.HasPrefix(path, container+"/") {
			rel := strings.TrimPrefix(path[len(container):], "/")
			return &mappings[indexOf(mappings, *m)], rel
		}
	}
	return nil, ""
}

func indexOf(mappings []PathMapping, m PathMapping) int {
	for i, x := range mappings {
		if x.ContainerPath == m.ContainerPath && x.LocalPath == m.LocalPath {
			return i
		}
	}
	return 0
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
	cleaned, err := filepath.Abs(filepath.FromSlash(path))
	if err != nil {
		cleaned = path
	}

	sorted := make([]PathMapping, len(mappings))
	copy(sorted, mappings)
	sort.SliceStable(sorted, func(i, j int) bool {
		ai, _ := filepath.Abs(sorted[i].LocalPath)
		aj, _ := filepath.Abs(sorted[j].LocalPath)
		return len(ai) > len(aj)
	})

	for _, m := range sorted {
		local, err := filepath.Abs(m.LocalPath)
		if err != nil {
			continue
		}
		if cleaned == local {
			return m.ContainerPath
		}
		if strings.HasPrefix(cleaned, local+string(filepath.Separator)) {
			rel := strings.TrimPrefix(cleaned[len(local):], string(filepath.Separator))
			rel = filepath.ToSlash(rel)
			if rel == "" {
				return m.ContainerPath
			}
			return strings.TrimRight(m.ContainerPath, "/") + "/" + rel
		}
	}
	return cleaned
}

// reverseResolvePathsInOutput rewrites host paths back to container paths in arbitrary text.
func reverseResolvePathsInOutput(mappings []PathMapping, output string) string {
	if len(mappings) == 0 || output == "" {
		return output
	}
	sorted := make([]PathMapping, len(mappings))
	copy(sorted, mappings)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].LocalPath) > len(sorted[j].LocalPath)
	})
	result := output
	for _, m := range sorted {
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
	sorted := make([]PathMapping, len(mappings))
	copy(sorted, mappings)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].ContainerPath) > len(sorted[j].ContainerPath)
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
	cleaned, err := filepath.Abs(resolvedPath)
	if err != nil {
		cleaned = resolvedPath
	}
	var best *PathMapping
	bestLen := -1
	for i := range mappings {
		local, err := filepath.Abs(mappings[i].LocalPath)
		if err != nil {
			continue
		}
		if cleaned == local || strings.HasPrefix(cleaned, local+string(filepath.Separator)) {
			if len(local) > bestLen {
				bestLen = len(local)
				best = &mappings[i]
			}
		}
	}
	if best == nil {
		return false
	}
	return best.ReadOnly
}
