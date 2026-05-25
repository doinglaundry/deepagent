package local

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandboxpaths"
)

type resolved struct {
	Path    string
	Mapping *sandboxpaths.MountMapping
}

func resolvePath(mappings []sandboxpaths.MountMapping, path string) (resolved, error) {
	r, err := sandboxpaths.ResolveHostPath(mappings, path)
	if err != nil {
		if strings.Contains(err.Error(), "path escapes mount root") {
			return resolved{}, sandbox.NewPermissionError("path escapes mount root", path)
		}
		return resolved{}, err
	}
	return resolved{Path: r.HostPath, Mapping: r.Mapping}, nil
}

func resolvePathsInCommand(mappings []sandboxpaths.MountMapping, command string) string {
	return rewriteVirtualPaths(mappings, command, `(?:/|$|[\s"';&|<>()])`, false)
}

func resolvePathsInContent(mappings []sandboxpaths.MountMapping, content string) string {
	return rewriteVirtualPaths(mappings, content, `(?:/|$|[^\w./-])`, true)
}

func rewriteVirtualPaths(mappings []sandboxpaths.MountMapping, src, boundary string, forwardSlash bool) string {
	if len(mappings) == 0 || src == "" {
		return src
	}
	sorted := sortedMappings(mappings, func(a, b sandboxpaths.MountMapping) bool {
		return len(a.VirtualPath) > len(b.VirtualPath)
	})

	var alts []string
	for _, m := range sorted {
		alts = append(alts, "("+regexp.QuoteMeta(m.VirtualPath)+boundary+`(?:/[^\s"';&|<>()]*)?`+")")
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
			if strings.HasPrefix(core, m.VirtualPath) {
				rest := core[len(m.VirtualPath):]
				if rest == "" {
					return m.HostPath
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
				return m.HostPath + rest
			}
		}
		return match
	})
}

func isReadOnlyPath(mappings []sandboxpaths.MountMapping, resolvedPath string) bool {
	m, _ := findHostPathMappingForResolved(mappings, absPath(resolvedPath))
	return m != nil && m.ReadOnly
}

func findHostPathMappingForResolved(mappings []sandboxpaths.MountMapping, path string) (*sandboxpaths.MountMapping, string) {
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
	if strings.HasPrefix(path, hostPath+string(filepath.Separator)) {
		return strings.TrimPrefix(path[len(hostPath):], string(filepath.Separator)), true
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

func isUnder(child, parent string) bool {
	if child == parent {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, parent+sep)
}

func sortedMappings(mappings []sandboxpaths.MountMapping, less func(a, b sandboxpaths.MountMapping) bool) []sandboxpaths.MountMapping {
	sorted := append([]sandboxpaths.MountMapping(nil), mappings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return less(sorted[i], sorted[j])
	})
	return sorted
}
